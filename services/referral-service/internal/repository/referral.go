package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vpn/referral-service/internal/model"
)

// ErrTokenNotFound — реферальный токен не существует.
var ErrTokenNotFound = errors.New("referral token not found")

// ErrUserNotFound — нет такого юзера в users.
var ErrUserNotFound = errors.New("user not found")

// ErrAlreadyInvited — у invited уже есть inviter (UNIQUE constraint).
var ErrAlreadyInvited = errors.New("user already has an inviter")

// ErrInsufficientBalance — недостаточно средств для вывода.
var ErrInsufficientBalance = errors.New("insufficient balance")

type Repository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// ─── User helpers ───────────────────────────────────────────────────

// UserBrief — минимум полей из users, нужный для anti-abuse и роутинга бонусов.
type UserBrief struct {
	ID        int64
	Role      string
	Balance   float64
	CreatedAt time.Time
}

func (r *Repository) GetUserByID(ctx context.Context, userID int64) (*UserBrief, error) {
	const q = `SELECT id, role, balance, created_at FROM users WHERE id = $1`
	u := &UserBrief{}
	if err := r.db.QueryRow(ctx, q, userID).Scan(&u.ID, &u.Role, &u.Balance, &u.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

// ─── Referral Links ─────────────────────────────────────────────────

// GetOrCreateLink — атомарно получить существующий или создать новый токен.
// Если у юзера уже есть запись — возвращает её. Если нет — INSERT с переданным
// токеном, защита от коллизий через UNIQUE на token.
func (r *Repository) GetOrCreateLink(ctx context.Context, userID int64, newToken string) (*model.ReferralLink, error) {
	// Сначала пробуем читать (hot path: ссылка уже создана).
	if link, err := r.getLinkByUserID(ctx, userID); err == nil {
		return link, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// Не нашли — INSERT. ON CONFLICT (user_id) DO NOTHING защищает от race
	// между параллельными запросами, RETURNING * вернёт либо нашу новую строку,
	// либо ничего (на конфликте). В случае ничего — повторяем select.
	link := &model.ReferralLink{}
	err := r.db.QueryRow(ctx, `
		INSERT INTO referral_links (user_id, token)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO NOTHING
		RETURNING id, user_id, token, click_count, created_at
	`, userID, newToken).Scan(&link.ID, &link.UserID, &link.Token, &link.ClickCount, &link.CreatedAt)
	if err == nil {
		return link, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("insert referral link: %w", err)
	}
	// На конфликте RETURNING ничего не отдал — значит уже есть, читаем.
	return r.getLinkByUserID(ctx, userID)
}

func (r *Repository) getLinkByUserID(ctx context.Context, userID int64) (*model.ReferralLink, error) {
	const q = `SELECT id, user_id, token, click_count, created_at FROM referral_links WHERE user_id = $1`
	link := &model.ReferralLink{}
	if err := r.db.QueryRow(ctx, q, userID).Scan(&link.ID, &link.UserID, &link.Token, &link.ClickCount, &link.CreatedAt); err != nil {
		return nil, err
	}
	return link, nil
}

// GetLinkByToken — нужно для RegisterReferral / RegisterClick.
func (r *Repository) GetLinkByToken(ctx context.Context, token string) (*model.ReferralLink, error) {
	const q = `SELECT id, user_id, token, click_count, created_at FROM referral_links WHERE token = $1`
	link := &model.ReferralLink{}
	if err := r.db.QueryRow(ctx, q, token).Scan(&link.ID, &link.UserID, &link.Token, &link.ClickCount, &link.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTokenNotFound
		}
		return nil, fmt.Errorf("get link by token: %w", err)
	}
	return link, nil
}

func (r *Repository) IncrementClicks(ctx context.Context, token string) (int32, error) {
	var clicks int32
	err := r.db.QueryRow(ctx, `
		UPDATE referral_links SET click_count = click_count + 1
		WHERE token = $1
		RETURNING click_count
	`, token).Scan(&clicks)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrTokenNotFound
		}
		return 0, fmt.Errorf("increment clicks: %w", err)
	}
	return clicks, nil
}

// ─── Relationships ──────────────────────────────────────────────────

// CreateRelationship — INSERT с проверкой self-invite (CHECK в схеме) и UNIQUE
// invited_id. На конфликте по invited_id возвращает ErrAlreadyInvited.
func (r *Repository) CreateRelationship(ctx context.Context, inviterID, invitedID int64) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO referral_relationships (inviter_id, invited_id, status)
		VALUES ($1, $2, 'registered')
	`, inviterID, invitedID)
	if err != nil {
		// pg uniq violation на invited_id или PK.
		if isUniqueViolation(err) {
			return ErrAlreadyInvited
		}
		return fmt.Errorf("create relationship: %w", err)
	}
	return nil
}

func (r *Repository) MarkRelationshipPurchased(ctx context.Context, invitedID int64) error {
	_, err := r.db.Exec(ctx, `
		UPDATE referral_relationships SET status = 'purchased'
		WHERE invited_id = $1 AND status = 'registered'
	`, invitedID)
	if err != nil {
		return fmt.Errorf("mark purchased: %w", err)
	}
	return nil
}

// GetRelationshipByInvited возвращает inviter_id и status или nil если связи нет.
func (r *Repository) GetRelationshipByInvited(ctx context.Context, invitedID int64) (*model.ReferralRelationship, error) {
	rel := &model.ReferralRelationship{}
	err := r.db.QueryRow(ctx, `
		SELECT inviter_id, invited_id, status, created_at
		FROM referral_relationships WHERE invited_id = $1
	`, invitedID).Scan(&rel.InviterID, &rel.InvitedID, &rel.Status, &rel.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get relationship: %w", err)
	}
	return rel, nil
}

// CountReferrals — для статистики. invitedCount = всего, purchasedCount = из них с покупкой.
func (r *Repository) CountReferrals(ctx context.Context, inviterID int64) (invitedCount, purchasedCount int32, err error) {
	row := r.db.QueryRow(ctx, `
		SELECT
		  COUNT(*) FILTER (WHERE inviter_id = $1) AS total,
		  COUNT(*) FILTER (WHERE inviter_id = $1 AND status = 'purchased') AS purchased
		FROM referral_relationships
	`, inviterID)
	if err = row.Scan(&invitedCount, &purchasedCount); err != nil {
		return 0, 0, fmt.Errorf("count referrals: %w", err)
	}
	return invitedCount, purchasedCount, nil
}

// ─── Bonuses ────────────────────────────────────────────────────────

// CreateBonus — единичная запись. paymentID=nil для бонусов при регистрации.
// Заполняет b.ID и b.CreatedAt после INSERT.
// UNIQUE по payment_id WHERE NOT NULL → повторный INSERT с тем же paymentID
// возвращает ErrPaymentBonusExists.
func (r *Repository) CreateBonus(ctx context.Context, b *model.ReferralBonus) error {
	err := r.db.QueryRow(ctx, `
		INSERT INTO referral_bonuses
		    (user_id, invited_user_id, bonus_type, days_amount, balance_amount, payment_id, is_applied)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at
	`, b.UserID, b.InvitedUserID, b.BonusType, b.DaysAmount, b.BalanceAmount, b.PaymentID, b.IsApplied).
		Scan(&b.ID, &b.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrPaymentBonusExists
		}
		return fmt.Errorf("create bonus: %w", err)
	}
	return nil
}

// ErrPaymentBonusExists — бонус для этого payment_id уже создан (идемпотентность).
var ErrPaymentBonusExists = errors.New("bonus for this payment already exists")

// MarkBonusApplied — устанавливает is_applied=true. Идемпотентно.
func (r *Repository) MarkBonusApplied(ctx context.Context, bonusID int64) error {
	_, err := r.db.Exec(ctx, `UPDATE referral_bonuses SET is_applied = true WHERE id = $1`, bonusID)
	if err != nil {
		return fmt.Errorf("mark bonus applied: %w", err)
	}
	return nil
}

// SumStatsByUser возвращает total дней (для role=user) и total ₽ (для role=partner).
func (r *Repository) SumStatsByUser(ctx context.Context, userID int64) (rewardedDays int32, earnedBalance float64, err error) {
	row := r.db.QueryRow(ctx, `
		SELECT
		  COALESCE(SUM(days_amount) FILTER (WHERE bonus_type = 'days'), 0)::int,
		  COALESCE(SUM(balance_amount) FILTER (WHERE bonus_type = 'balance'), 0)::float8
		FROM referral_bonuses
		WHERE user_id = $1
	`, userID)
	if err = row.Scan(&rewardedDays, &earnedBalance); err != nil {
		return 0, 0, fmt.Errorf("sum stats: %w", err)
	}
	return rewardedDays, earnedBalance, nil
}

// ─── Users mutations (нужны для бонусов) ────────────────────────────

// AddPendingBonusDays — увеличивает users.pending_bonus_days атомарно.
// Используется когда у юзера нет активной подписки в момент начисления.
func (r *Repository) AddPendingBonusDays(ctx context.Context, userID int64, days int32) error {
	_, err := r.db.Exec(ctx, `
		UPDATE users SET pending_bonus_days = COALESCE(pending_bonus_days, 0) + $2
		WHERE id = $1
	`, userID, days)
	if err != nil {
		return fmt.Errorf("add pending_bonus_days: %w", err)
	}
	return nil
}

// AddBalance — пополняет users.balance (для партнёрских 30%).
func (r *Repository) AddBalance(ctx context.Context, userID int64, amount float64) error {
	_, err := r.db.Exec(ctx, `
		UPDATE users SET balance = balance + $2
		WHERE id = $1
	`, userID, amount)
	if err != nil {
		return fmt.Errorf("add balance: %w", err)
	}
	return nil
}

// ─── Withdrawals ────────────────────────────────────────────────────

// CreateWithdrawalTx атомарно списывает amount с users.balance и создаёт
// заявку. Возвращает ErrInsufficientBalance если баланс < amount.
func (r *Repository) CreateWithdrawalTx(ctx context.Context, userID int64, amount float64, method string, details map[string]string) (*model.WithdrawalRequest, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var balance float64
	if err := tx.QueryRow(ctx, `SELECT balance FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&balance); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("lock user: %w", err)
	}
	if balance < amount {
		return nil, ErrInsufficientBalance
	}

	if _, err := tx.Exec(ctx, `UPDATE users SET balance = balance - $2 WHERE id = $1`, userID, amount); err != nil {
		return nil, fmt.Errorf("deduct balance: %w", err)
	}

	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return nil, fmt.Errorf("marshal details: %w", err)
	}

	wr := &model.WithdrawalRequest{}
	var detailsBytes []byte
	err = tx.QueryRow(ctx, `
		INSERT INTO withdrawal_requests (user_id, amount, payment_method, payment_details)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, amount, payment_method, payment_details, status, admin_comment, created_at, processed_at
	`, userID, amount, method, detailsJSON).Scan(
		&wr.ID, &wr.UserID, &wr.Amount, &wr.PaymentMethod, &detailsBytes,
		&wr.Status, &wr.AdminComment, &wr.CreatedAt, &wr.ProcessedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert withdrawal: %w", err)
	}
	if err := json.Unmarshal(detailsBytes, &wr.PaymentDetails); err != nil {
		wr.PaymentDetails = map[string]string{}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return wr, nil
}

// ListWithdrawals возвращает заявки. userID=0 → все (для админки).
// status="" → все статусы.
func (r *Repository) ListWithdrawals(ctx context.Context, userID int64, status string, limit, offset int32) ([]*model.WithdrawalRequest, int32, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	args := []interface{}{}
	where := "WHERE 1=1"
	if userID > 0 {
		args = append(args, userID)
		where += fmt.Sprintf(" AND user_id = $%d", len(args))
	}
	if status != "" {
		args = append(args, status)
		where += fmt.Sprintf(" AND status = $%d", len(args))
	}

	var total int32
	if err := r.db.QueryRow(ctx, "SELECT COUNT(*) FROM withdrawal_requests "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count withdrawals: %w", err)
	}

	args = append(args, limit, offset)
	q := fmt.Sprintf(`
		SELECT id, user_id, amount, payment_method, payment_details, status, admin_comment, created_at, processed_at
		FROM withdrawal_requests
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, len(args)-1, len(args))

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list withdrawals: %w", err)
	}
	defer rows.Close()

	var out []*model.WithdrawalRequest
	for rows.Next() {
		wr := &model.WithdrawalRequest{}
		var detailsBytes []byte
		if err := rows.Scan(&wr.ID, &wr.UserID, &wr.Amount, &wr.PaymentMethod, &detailsBytes,
			&wr.Status, &wr.AdminComment, &wr.CreatedAt, &wr.ProcessedAt); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(detailsBytes, &wr.PaymentDetails); err != nil {
			wr.PaymentDetails = map[string]string{}
		}
		out = append(out, wr)
	}
	return out, total, nil
}

// isUniqueViolation определяет нарушение UNIQUE constraint в Postgres.
// Используем код 23505 (unique_violation), без захвата конкретной таблицы —
// репозиторий сам знает контекст (откуда вернулась ошибка) и интерпретирует.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// pgx возвращает *pgconn.PgError; удобнее искать SQLSTATE по строке,
	// чтобы не тащить лишний import.
	return containsSQLState(err.Error(), "23505")
}

func containsSQLState(msg, code string) bool {
	// pgx форматирует ошибку как "ERROR: ... (SQLSTATE 23505)"
	for i := 0; i < len(msg)-len(code); i++ {
		if msg[i:i+len(code)] == code {
			return true
		}
	}
	return false
}
