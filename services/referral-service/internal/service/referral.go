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

// buildDeepLink — реферальная ссылка через классический bot deep-link
// (https://core.telegram.org/api/links#bot-links).
//
//	https://t.me/<BotUsername>?start=ref_<token>
//
// Telegram при клике откроет чат с ботом и пошлёт ему "/start ref_<token>".
// Webhook-хендлер бота (gateway/handler/telegram_bot.go::handleStart)
// извлекает токен и через auth-service сохраняет pending-атрибуцию по
// telegram_id. На первой регистрации юзера в Mini App
// (auth-service.ValidateTelegramUser) этот токен подхватывается и
// инициирует RegisterReferral в референц-сервисе.
//
// Почему НЕ ?startapp=ref_<token>: формат `?startapp=` требует, чтобы у
// бота в @BotFather был сконфигурирован "main mini app" — иначе Telegram
// просто открывает чат и не запускает Mini App автоматически. Классический
// `?start=` работает универсально (Mobile/Desktop/Web).
func (s *Referral) buildDeepLink(token string) string {
	return fmt.Sprintf("https://t.me/%s?start=ref_%s", s.cfg.BotUsername, token)
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
//
// Поля Applied/AlreadyApplied/NoRelationship/InviterUserID/InviterRole/BalanceAmount
// относятся к классической реферальной программе (ref_<token>).
//
// Поля Campaign* — ПАРАЛЛЕЛЬНАЯ выплата по маркетинговой кампании (src_<slug>).
// Юзер может одновременно быть и приглашённым (ref-token), и атрибутированным
// к кампании (src-link). Тогда ApplyBonus начисляет ОБЕ выплаты, независимо.
// На практике юзеры обычно идут одним путём — но архитектурно это два разных
// канала и они не конкурируют.
type ApplyBonusResult struct {
	Applied        bool
	AlreadyApplied bool
	NoRelationship bool
	InviterUserID  int64
	InviterRole    string
	BalanceAmount  float64

	// Кампания-атрибуция (опционально).
	CampaignPayoutApplied        bool
	CampaignPayoutAlreadyApplied bool
	CampaignID                   int64
	CampaignPartnerUserID        int64
	CampaignPayoutAmount         float64
}

// ApplyBonus — вызывается из payment-service после успешной первой оплаты
// приглашённого. Логика:
//
//  1. Найти relationship по invited_id для классического реф-канала
//     (если нет — пропускаем эту ветку, но кампанию проверяем всё равно).
//  2. Проверить идемпотентность по payment_id (UNIQUE на referral_bonuses.payment_id).
//  3. Если inviter.role='partner' → начислить процент на баланс (ref-канал).
//  4. UPDATE relationship SET status='purchased' (один раз, idempotent).
//  5. ПАРАЛЛЕЛЬНО: проверить campaign-attribution. Если у юзера есть
//     user_attribution и у кампании задан payout_percent → начислить
//     процент партнёру кампании (campaign_payouts UNIQUE на payment_id).
//
// Для inviter.role='user' бонус уже выдан при регистрации — здесь только
// маркируем relationship как purchased.
//
// Ref- и campaign-каналы идемпотентны независимо: даже если ref-bonus уже
// был применён (AlreadyApplied=true), мы всё равно проверяем кампанию.
// Это нужно потому что webhook-ретрай может произойти после того как ref
// уже зачислился, а campaign-payout (например, добавленный через миграцию
// данных позже) — нет.
func (s *Referral) ApplyBonus(ctx context.Context, invitedID int64, amountRUB float64, paymentID int64) (*ApplyBonusResult, error) {
	if invitedID <= 0 || amountRUB <= 0 || paymentID <= 0 {
		return nil, errors.New("invalid arguments")
	}

	res := &ApplyBonusResult{}

	// ─── Ref-канал ─────────────────────────────────────────────
	rel, err := s.repo.GetRelationshipByInvited(ctx, invitedID)
	if err != nil {
		return nil, err
	}
	if rel == nil {
		res.NoRelationship = true
	} else {
		inviter, err := s.repo.GetUserByID(ctx, rel.InviterID)
		if err != nil {
			return nil, err
		}
		res.InviterUserID = rel.InviterID
		res.InviterRole = inviter.Role

		if inviter.Role == "partner" && s.cfg.PartnerPercent > 0 {
			amount := roundCents(amountRUB * float64(s.cfg.PartnerPercent) / 100.0)
			bonus := &model.ReferralBonus{
				UserID:        rel.InviterID,
				InvitedUserID: invitedID,
				BonusType:     model.BonusTypeBalance,
				BalanceAmount: &amount,
				PaymentID:     &paymentID,
				IsApplied:     false,
			}
			err := s.repo.CreateBonus(ctx, bonus)
			switch {
			case errors.Is(err, repository.ErrPaymentBonusExists):
				res.AlreadyApplied = true
			case err != nil:
				return nil, fmt.Errorf("create partner bonus: %w", err)
			default:
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
		}

		// Маркер purchased — независимо от того, прошёл ли бонус.
		if rel.Status != model.RelationshipStatusPurchased {
			if err := s.repo.MarkRelationshipPurchased(ctx, invitedID); err != nil {
				s.log.Warn("mark relationship purchased failed", zap.Error(err))
			}
		}
	}

	// ─── Campaign-канал (параллельно ref'у) ───────────────────
	s.applyCampaignPayout(ctx, invitedID, amountRUB, paymentID, res)

	return res, nil
}

// applyCampaignPayout — параллельная ветка ApplyBonus. Если у платящего юзера
// есть user_attribution к кампании с payout_percent — начисляем партнёру
// кампании на баланс. Идемпотентность по payment_id (UNIQUE в campaign_payouts).
//
// Все ошибки логируются, но НЕ роняют ApplyBonus — ref-канал важнее, чтобы
// корректно вернуть NoRelationship/Applied для существующих юзеров.
func (s *Referral) applyCampaignPayout(ctx context.Context, invitedID int64, amountRUB float64, paymentID int64, res *ApplyBonusResult) {
	attr, err := s.repo.GetCampaignAttribution(ctx, invitedID)
	if err != nil {
		s.log.Warn("get campaign attribution failed (non-blocking)",
			zap.Int64("invited_id", invitedID),
			zap.Error(err),
		)
		return
	}
	if attr == nil {
		return // нет атрибуции или у кампании нет выплат
	}
	res.CampaignID = attr.CampaignID
	res.CampaignPartnerUserID = attr.PartnerUserID

	amount := roundCents(amountRUB * float64(attr.PayoutPercent) / 100.0)
	if amount <= 0 {
		return
	}

	payoutID, err := s.repo.CreateCampaignPayout(ctx, attr.CampaignID, attr.PartnerUserID, invitedID, paymentID, amount)
	if errors.Is(err, repository.ErrCampaignPayoutExists) {
		res.CampaignPayoutAlreadyApplied = true
		s.log.Info("campaign payout already applied",
			zap.Int64("campaign_id", attr.CampaignID),
			zap.Int64("payment_id", paymentID),
		)
		return
	}
	if err != nil {
		s.log.Error("create campaign payout failed",
			zap.Int64("campaign_id", attr.CampaignID),
			zap.Int64("payment_id", paymentID),
			zap.Error(err),
		)
		return
	}

	if err := s.repo.AddBalance(ctx, attr.PartnerUserID, amount); err != nil {
		s.log.Error("add campaign partner balance failed",
			zap.Int64("partner_id", attr.PartnerUserID),
			zap.Int64("payout_id", payoutID),
			zap.Float64("amount", amount),
			zap.Error(err),
		)
		// Запись осталась с is_applied=false — можно ретрайнуть позже.
		return
	}
	_ = s.repo.MarkCampaignPayoutApplied(ctx, payoutID)

	res.CampaignPayoutApplied = true
	res.CampaignPayoutAmount = amount
	s.log.Info("campaign payout applied",
		zap.Int64("campaign_id", attr.CampaignID),
		zap.Int64("partner_id", attr.PartnerUserID),
		zap.Int64("invited_id", invitedID),
		zap.Int64("payment_id", paymentID),
		zap.Float64("amount", amount),
	)
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
