package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vpn/referral-service/internal/config"
	"github.com/vpn/referral-service/internal/model"
	"github.com/vpn/referral-service/internal/repository"
	"github.com/vpn/referral-service/internal/token"
	subpb "github.com/vpn/shared/pkg/proto/subscription/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// SubClient — узкий интерфейс к subscription-service. Только то, что нужно
// сервису. Подходит как сгенерированный gRPC клиент, так и mock в тестах.
type SubClient interface {
	ApplyBonusDays(ctx context.Context, req *subpb.ApplyBonusDaysRequest, opts ...grpc.CallOption) (*subpb.ApplyBonusDaysResponse, error)
}

// LinkInfo — собранный результат GetOrCreateLink с полным URL.
type LinkInfo struct {
	URL        string
	Token      string
	ClickCount int32
}

// Referral — главный сервис реферальной программы.
type Referral struct {
	repo *repository.Repository
	sub  SubClient
	cfg  config.ReferralConfig
	log  *zap.Logger
}

func New(repo *repository.Repository, sub SubClient, cfg config.ReferralConfig, log *zap.Logger) *Referral {
	return &Referral{repo: repo, sub: sub, cfg: cfg, log: log}
}

// ─── GetOrCreateLink ────────────────────────────────────────────────

// GetOrCreateLink идемпотентно возвращает реферальную ссылку юзера.
// Конкурентный INSERT защищён ON CONFLICT в репозитории. На случай (теоретической)
// коллизии токенов делаем до 5 попыток.
func (s *Referral) GetOrCreateLink(ctx context.Context, userID int64) (*LinkInfo, error) {
	if userID <= 0 {
		return nil, errors.New("invalid user_id")
	}

	const maxAttempts = 5
	var link *model.ReferralLink
	for attempt := 0; attempt < maxAttempts; attempt++ {
		newToken, err := token.Generate(0)
		if err != nil {
			return nil, fmt.Errorf("generate token: %w", err)
		}
		link, err = s.repo.GetOrCreateLink(ctx, userID, newToken)
		if err == nil {
			break
		}
		// На UNIQUE-конфликте по token (теоретически) — пробуем ещё раз с другим.
		if attempt == maxAttempts-1 {
			return nil, fmt.Errorf("get or create link: %w", err)
		}
	}

	return &LinkInfo{
		URL:        s.buildDeepLink(link.Token),
		Token:      link.Token,
		ClickCount: link.ClickCount,
	}, nil
}

func (s *Referral) buildDeepLink(token string) string {
	return fmt.Sprintf("https://t.me/%s?startapp=ref_%s", s.cfg.BotUsername, token)
}

// ─── RegisterClick ──────────────────────────────────────────────────

func (s *Referral) RegisterClick(ctx context.Context, tok string) (found bool, clicks int32, err error) {
	if !token.IsValid(tok) {
		return false, 0, nil
	}
	c, err := s.repo.IncrementClicks(ctx, tok)
	if err != nil {
		if errors.Is(err, repository.ErrTokenNotFound) {
			return false, 0, nil
		}
		return false, 0, err
	}
	return true, c, nil
}

// ─── RegisterReferral ───────────────────────────────────────────────

// RegisterResult — детали что произошло при попытке регистрации реферала.
type RegisterResult struct {
	Registered         bool
	InviterUserID      int64
	SkipReason         string
	InviterDaysAwarded int32
	InvitedDaysAwarded int32
}

// RegisterReferral — основной anti-abuse + бизнес-логика.
//
//  1. Валидация токена и поиск inviter'а
//  2. Проверки: self-invite, freshness invited, уникальность
//  3. INSERT relationship (если упало — already_invited)
//  4. Начисление бонусов (записи в referral_bonuses + RPC к sub-service)
//
// Любая ошибка от sub-service НЕ откатывает relationship — бонус остаётся
// is_applied=false и может быть выдан повторно через ретрай / админку.
func (s *Referral) RegisterReferral(ctx context.Context, inviterToken string, invitedID int64) (*RegisterResult, error) {
	if !token.IsValid(inviterToken) {
		return &RegisterResult{SkipReason: model.SkipReasonTokenNotFound}, nil
	}
	if invitedID <= 0 {
		return nil, errors.New("invalid invited_user_id")
	}

	// 1. Inviter token → inviter user_id.
	link, err := s.repo.GetLinkByToken(ctx, inviterToken)
	if err != nil {
		if errors.Is(err, repository.ErrTokenNotFound) {
			return &RegisterResult{SkipReason: model.SkipReasonTokenNotFound}, nil
		}
		return nil, err
	}
	inviterID := link.UserID

	// 2a. Self-invite — отсекаем сразу.
	if inviterID == invitedID {
		return &RegisterResult{SkipReason: model.SkipReasonSelfInvite}, nil
	}

	// 2b. Проверяем что invited существует и юный (created_at < freshness).
	invited, err := s.repo.GetUserByID(ctx, invitedID)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return &RegisterResult{SkipReason: model.SkipReasonInvitedNotFound}, nil
		}
		return nil, err
	}
	if time.Since(invited.CreatedAt) > time.Duration(s.cfg.FreshnessSeconds)*time.Second {
		s.log.Info("referral skipped: user too old",
			zap.Int64("invited_id", invitedID),
			zap.Time("created_at", invited.CreatedAt),
			zap.Int("freshness_seconds", s.cfg.FreshnessSeconds),
		)
		return &RegisterResult{SkipReason: model.SkipReasonUserTooOld}, nil
	}

	// 2c. Inviter — для роутинга бонусов нам нужна его роль.
	inviter, err := s.repo.GetUserByID(ctx, inviterID)
	if err != nil {
		// inviter существует — он же владелец токена — но если упало,
		// прокидываем ошибку наверх.
		return nil, err
	}

	// 3. Создаём связь.
	if err := s.repo.CreateRelationship(ctx, inviterID, invitedID); err != nil {
		if errors.Is(err, repository.ErrAlreadyInvited) {
			return &RegisterResult{SkipReason: model.SkipReasonAlreadyInvited, InviterUserID: inviterID}, nil
		}
		return nil, fmt.Errorf("create relationship: %w", err)
	}

	res := &RegisterResult{Registered: true, InviterUserID: inviterID}

	// 4a. Бонус приглашённому — всегда +N дней (он только что зарегался).
	res.InvitedDaysAwarded = s.applyDaysBonus(ctx, invitedID, inviterID, s.cfg.BonusDays, "invited_registration")

	// 4b. Бонус пригласителю — только если он role='user' (не партнёр).
	// Партнёры получают свои деньги при покупке, не при регистрации.
	if inviter.Role != "partner" {
		res.InviterDaysAwarded = s.applyDaysBonus(ctx, inviterID, invitedID, s.cfg.BonusDays, "inviter_registration")
	} else {
		s.log.Info("inviter is partner, deferring bonus until purchase",
			zap.Int64("inviter_id", inviterID),
			zap.Int64("invited_id", invitedID),
		)
	}

	s.log.Info("referral registered",
		zap.Int64("inviter_id", inviterID),
		zap.Int64("invited_id", invitedID),
		zap.String("inviter_role", inviter.Role),
		zap.Int32("inviter_days", res.InviterDaysAwarded),
		zap.Int32("invited_days", res.InvitedDaysAwarded),
	)
	return res, nil
}

// applyDaysBonus — записывает бонус в referral_bonuses и вызывает sub-service
// для начисления дней. Возвращает количество фактически начисленных дней
// (0 если что-то упало, но запись в БД при этом останется is_applied=false
// для будущих ретраев).
func (s *Referral) applyDaysBonus(ctx context.Context, recipientID, invitedID int64, days int32, reason string) int32 {
	if days <= 0 {
		return 0
	}

	bonus := &model.ReferralBonus{
		UserID:        recipientID,
		InvitedUserID: invitedID,
		BonusType:     model.BonusTypeDays,
		DaysAmount:    &days,
		IsApplied:     false,
	}
	// CreateBonus не падает на ON CONFLICT для регистрационных бонусов
	// (payment_id=NULL → UNIQUE WHERE NOT NULL не цепляется).
	if err := s.repo.CreateBonus(ctx, bonus); err != nil {
		s.log.Error("create days bonus failed",
			zap.Int64("recipient_id", recipientID),
			zap.String("reason", reason),
			zap.Error(err),
		)
		return 0
	}

	// Best-effort RPC. Если sub-service упал — бонус останется
	// is_applied=false, можно дёрнуть повторно.
	resp, err := s.sub.ApplyBonusDays(ctx, &subpb.ApplyBonusDaysRequest{
		UserId: recipientID,
		Days:   days,
	})
	if err != nil {
		s.log.Error("subscription.ApplyBonusDays failed",
			zap.Int64("recipient_id", recipientID),
			zap.String("reason", reason),
			zap.Error(err),
		)
		return 0
	}

	// На успехе помечаем бонус is_applied=true (для аудита).
	// (Скрываем ошибку — это просто аудит.)
	_ = s.repo.MarkBonusApplied(ctx, bonus.ID)

	if resp.AppliedToSubscription {
		s.log.Info("bonus applied to subscription",
			zap.Int64("recipient_id", recipientID),
			zap.Int32("days", days))
	} else {
		s.log.Info("bonus stored in pending_bonus_days",
			zap.Int64("recipient_id", recipientID),
			zap.Int32("days", days),
			zap.Int32("pending_total", resp.PendingDaysTotal))
	}
	return days
}



// ─── ApplyBonus (на покупку) ────────────────────────────────────────

// ApplyBonusResult — детали начисления партнёрского бонуса.
type ApplyBonusResult struct {
	Applied        bool
	AlreadyApplied bool
	NoRelationship bool
	InviterUserID  int64
	InviterRole    string
	BalanceAmount  float64
}

// ApplyBonus — вызывается из payment-service после успешной первой оплаты
// приглашённого. Логика:
//
//  1. Найти relationship по invited_id; если нет — return NoRelationship
//  2. Проверить идемпотентность по payment_id (UNIQUE на referral_bonuses.payment_id)
//  3. Если inviter.role='partner' → начислить процент на баланс
//  4. UPDATE relationship SET status='purchased' (один раз, idempotent)
//
// Для inviter.role='user' бонус уже выдан при регистрации — здесь только
// маркируем relationship как purchased.
func (s *Referral) ApplyBonus(ctx context.Context, invitedID int64, amountRUB float64, paymentID int64) (*ApplyBonusResult, error) {
	if invitedID <= 0 || amountRUB <= 0 || paymentID <= 0 {
		return nil, errors.New("invalid arguments")
	}

	rel, err := s.repo.GetRelationshipByInvited(ctx, invitedID)
	if err != nil {
		return nil, err
	}
	if rel == nil {
		return &ApplyBonusResult{NoRelationship: true}, nil
	}

	inviter, err := s.repo.GetUserByID(ctx, rel.InviterID)
	if err != nil {
		return nil, err
	}

	res := &ApplyBonusResult{
		InviterUserID: rel.InviterID,
		InviterRole:   inviter.Role,
	}

	// Партнёрская часть: начислить процент. Идемпотентность по payment_id.
	if inviter.Role == "partner" && s.cfg.PartnerPercent > 0 {
		amount := amountRUB * float64(s.cfg.PartnerPercent) / 100.0
		// Округляем до копейки чтобы не плодить хвосты.
		amount = roundCents(amount)

		bonus := &model.ReferralBonus{
			UserID:        rel.InviterID,
			InvitedUserID: invitedID,
			BonusType:     model.BonusTypeBalance,
			BalanceAmount: &amount,
			PaymentID:     &paymentID,
			IsApplied:     false,
		}
		if err := s.repo.CreateBonus(ctx, bonus); err != nil {
			if errors.Is(err, repository.ErrPaymentBonusExists) {
				return &ApplyBonusResult{AlreadyApplied: true, InviterUserID: rel.InviterID, InviterRole: inviter.Role}, nil
			}
			return nil, fmt.Errorf("create partner bonus: %w", err)
		}

		// Зачисляем на баланс. Если упадёт — bonus останется is_applied=false.
		if err := s.repo.AddBalance(ctx, rel.InviterID, amount); err != nil {
			s.log.Error("add partner balance failed",
				zap.Int64("inviter_id", rel.InviterID),
				zap.Float64("amount", amount),
				zap.Error(err),
			)
			return nil, fmt.Errorf("add balance: %w", err)
		}
		_ = s.repo.MarkBonusApplied(ctx, bonus.ID)
		res.BalanceAmount = amount
		res.Applied = true

		s.log.Info("partner bonus applied",
			zap.Int64("inviter_id", rel.InviterID),
			zap.Int64("invited_id", invitedID),
			zap.Int64("payment_id", paymentID),
			zap.Float64("amount", amount),
		)
	}

	// Маркер purchased — для статистики (purchased_count) и чтобы понять
	// что приглашённый "конвертировался".
	if rel.Status != model.RelationshipStatusPurchased {
		if err := s.repo.MarkRelationshipPurchased(ctx, invitedID); err != nil {
			s.log.Warn("mark relationship purchased failed", zap.Error(err))
		}
	}

	return res, nil
}

// ─── GetReferralStats ───────────────────────────────────────────────

type Stats struct {
	InvitedCount         int32
	PurchasedCount       int32
	RewardedDaysTotal    int32
	EarnedBalanceTotal   float64
	CurrentBalance       float64
	PendingCount         int32 // приглашены, но не оплатили
}

func (s *Referral) GetStats(ctx context.Context, userID int64) (*Stats, error) {
	invited, purchased, err := s.repo.CountReferrals(ctx, userID)
	if err != nil {
		return nil, err
	}
	days, balance, err := s.repo.SumStatsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	user, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &Stats{
		InvitedCount:       invited,
		PurchasedCount:     purchased,
		RewardedDaysTotal:  days,
		EarnedBalanceTotal: balance,
		CurrentBalance:     user.Balance,
		PendingCount:       invited - purchased,
	}, nil
}

// ─── Withdrawals ────────────────────────────────────────────────────

type WithdrawalError string

const (
	WithdrawalErrInsufficient WithdrawalError = "insufficient_balance"
	WithdrawalErrNotPartner   WithdrawalError = "not_partner"
	WithdrawalErrTooSmall     WithdrawalError = "amount_too_small"
)

// CreateWithdrawal — создаёт заявку на вывод. Возвращает (request, errCode).
// Если errCode != "" — заявка не создана (request=nil).
func (s *Referral) CreateWithdrawal(ctx context.Context, userID int64, amount float64, method string, details map[string]string) (*model.WithdrawalRequest, WithdrawalError, error) {
	user, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	if user.Role != "partner" && user.Role != "admin" {
		return nil, WithdrawalErrNotPartner, nil
	}
	if amount < s.cfg.MinWithdrawalRUB {
		return nil, WithdrawalErrTooSmall, nil
	}
	wr, err := s.repo.CreateWithdrawalTx(ctx, userID, amount, method, details)
	if err != nil {
		if errors.Is(err, repository.ErrInsufficientBalance) {
			return nil, WithdrawalErrInsufficient, nil
		}
		return nil, "", err
	}
	s.log.Info("withdrawal request created",
		zap.Int64("user_id", userID),
		zap.Float64("amount", amount),
		zap.String("method", method),
	)
	return wr, "", nil
}

func (s *Referral) ListWithdrawals(ctx context.Context, userID int64, status string, limit, offset int32) ([]*model.WithdrawalRequest, int32, error) {
	return s.repo.ListWithdrawals(ctx, userID, status, limit, offset)
}

// roundCents округляет до 2 знаков после запятой.
func roundCents(x float64) float64 {
	return float64(int64(x*100+0.5)) / 100.0
}
