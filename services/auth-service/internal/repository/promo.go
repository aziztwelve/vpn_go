// Package repository — promo.go: персональные промо-токены.
//
// Таблица promo_codes (миграция 1777800000_add_promo_codes.up.sql):
// один токен = один user_id = одна одноразовая попытка купить со скидкой.
//
// Жизненный цикл:
//   - Issue(): INSERT promo_codes (token=random64, user_id, plan_id, ...)
//     UNIQUE-индекс uniq_promo_codes_active гарантирует, что у юзера на
//     один план не бывает двух активных (used_at IS NULL) промо.
//   - Redeem(): SELECT по token. Если used_at заполнен — already_used.
//   - AttachPayment(): после CreateInvoice сохраняем payment_id в строке.
//     Idempotent: повторный вызов с тем же payment_id — no-op.
//   - MarkUsed(): по webhook от платёжки — UPDATE used_at=NOW().
package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PromoRepository — доступ к таблице promo_codes.
//
// Хостится рядом с BroadcastRepository в auth-service: общая БД
// (users + promo_codes JOIN'ятся для админских команд).
type PromoRepository struct {
	db *pgxpool.Pool
}

func NewPromoRepository(db *pgxpool.Pool) *PromoRepository {
	return &PromoRepository{db: db}
}

// PromoCode — одна строка таблицы promo_codes.
type PromoCode struct {
	ID         int64
	Token      string
	UserID     int64
	PlanID     int32
	MaxDevices int32
	CreatedBy  *int64
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	UsedAt     *time.Time
	PaymentID  *int64
}

// ErrPromoNotFound — токен/(user,plan) не найден.
var ErrPromoNotFound = errors.New("promo code not found")

// IssueInput — параметры для Issue.
type IssueInput struct {
	UserID     int64
	PlanID     int32
	MaxDevices int32
	CreatedBy  int64         // 0 = NULL (например для системных issue без админа)
	TTL        time.Duration // 0 = бессрочно
}

// Issue выдаёт новый промо-код. Идемпотентность через UNIQUE-индекс:
// если у (user_id, plan_id) уже есть активный (used_at IS NULL) код —
// возвращает существующий.
//
// Возвращает (code, alreadyExisted, error). alreadyExisted=true когда
// мы наткнулись на существующий active промо и переиспользовали его.
func (r *PromoRepository) Issue(ctx context.Context, in IssueInput) (*PromoCode, bool, error) {
	if in.MaxDevices == 0 {
		in.MaxDevices = 2 // дефолт под device_addon_pricing(101, 2, 79.00)
	}

	// 1. Сначала пробуем найти существующий активный.
	existing, err := r.GetActiveByUserPlan(ctx, in.UserID, in.PlanID)
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, ErrPromoNotFound) {
		return nil, false, fmt.Errorf("lookup existing: %w", err)
	}

	// 2. Генерим уникальный токен. На UNIQUE-collision (вероятность 1/2^256
	//    при 32 байтах) ретраим до 3х раз.
	for attempt := 0; attempt < 3; attempt++ {
		token, err := generateToken()
		if err != nil {
			return nil, false, fmt.Errorf("generate token: %w", err)
		}

		var (
			expiresAt    *time.Time
			createdByVal interface{}
		)
		if in.TTL > 0 {
			t := time.Now().Add(in.TTL)
			expiresAt = &t
		}
		if in.CreatedBy != 0 {
			createdByVal = in.CreatedBy
		}

		var code PromoCode
		err = r.db.QueryRow(ctx, `
			INSERT INTO promo_codes
				(token, user_id, plan_id, max_devices, created_by, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, token, user_id, plan_id, max_devices,
			          created_by, created_at, expires_at, used_at, payment_id
		`,
			token, in.UserID, in.PlanID, in.MaxDevices,
			createdByVal, expiresAt,
		).Scan(
			&code.ID, &code.Token, &code.UserID, &code.PlanID, &code.MaxDevices,
			&code.CreatedBy, &code.CreatedAt, &code.ExpiresAt, &code.UsedAt, &code.PaymentID,
		)
		if err == nil {
			return &code, false, nil
		}

		// На уникальном конфликте по token — повторяем (1 в 2^256, всё же).
		// На конфликте по (user_id, plan_id) — race с параллельным Issue:
		// перечитываем и возвращаем существующий.
		if isUniqueViolation(err) {
			if existing, lookupErr := r.GetActiveByUserPlan(ctx, in.UserID, in.PlanID); lookupErr == nil {
				return existing, true, nil
			}
			// иначе возможно это token-collision, ретраим
			continue
		}
		return nil, false, fmt.Errorf("insert promo: %w", err)
	}
	return nil, false, fmt.Errorf("token collision after 3 retries (extremely unlikely)")
}

// GetByToken возвращает PromoCode по token. ErrPromoNotFound если нет.
func (r *PromoRepository) GetByToken(ctx context.Context, token string) (*PromoCode, error) {
	var c PromoCode
	err := r.db.QueryRow(ctx, `
		SELECT id, token, user_id, plan_id, max_devices,
		       created_by, created_at, expires_at, used_at, payment_id
		FROM promo_codes
		WHERE token = $1
	`, token).Scan(
		&c.ID, &c.Token, &c.UserID, &c.PlanID, &c.MaxDevices,
		&c.CreatedBy, &c.CreatedAt, &c.ExpiresAt, &c.UsedAt, &c.PaymentID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPromoNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get by token: %w", err)
	}
	return &c, nil
}

// GetActiveByUserPlan возвращает АКТИВНЫЙ (used_at IS NULL) промо для
// (user_id, plan_id). ErrPromoNotFound если нет такого.
func (r *PromoRepository) GetActiveByUserPlan(ctx context.Context, userID int64, planID int32) (*PromoCode, error) {
	var c PromoCode
	err := r.db.QueryRow(ctx, `
		SELECT id, token, user_id, plan_id, max_devices,
		       created_by, created_at, expires_at, used_at, payment_id
		FROM promo_codes
		WHERE user_id = $1 AND plan_id = $2 AND used_at IS NULL
		LIMIT 1
	`, userID, planID).Scan(
		&c.ID, &c.Token, &c.UserID, &c.PlanID, &c.MaxDevices,
		&c.CreatedBy, &c.CreatedAt, &c.ExpiresAt, &c.UsedAt, &c.PaymentID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPromoNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get active by user/plan: %w", err)
	}
	return &c, nil
}

// AttachPayment связывает promo с payment_id. Идемпотентность:
//   - если payment_id уже привязан И совпадает — no-op (ok=true)
//   - если уже привязан другой — error (защита от race)
//   - если NULL — UPDATE
//
// affected=1 при успехе (включая no-op match). 0 если token не найден.
func (r *PromoRepository) AttachPayment(ctx context.Context, token string, paymentID int64) error {
	if paymentID == 0 {
		return errors.New("payment_id required")
	}

	tag, err := r.db.Exec(ctx, `
		UPDATE promo_codes
		SET payment_id = $2
		WHERE token = $1
		  AND (payment_id IS NULL OR payment_id = $2)
	`, token, paymentID)
	if err != nil {
		return fmt.Errorf("attach payment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Либо токен не найден, либо payment_id уже занят другим. Делим.
		existing, lookupErr := r.GetByToken(ctx, token)
		if lookupErr != nil {
			return ErrPromoNotFound
		}
		return fmt.Errorf("token already attached to payment_id=%d (tried %d)",
			derefInt64(existing.PaymentID), paymentID)
	}
	return nil
}

// MarkUsedByPayment помечает promo как использованный по payment_id.
// matched=true если был найден и обновлён, false если payment не связан
// с промо (обычная оплата). promoID — для логирования.
func (r *PromoRepository) MarkUsedByPayment(ctx context.Context, paymentID int64) (matched bool, promoID int64, userID int64, err error) {
	err = r.db.QueryRow(ctx, `
		UPDATE promo_codes
		SET used_at = NOW()
		WHERE payment_id = $1 AND used_at IS NULL
		RETURNING id, user_id
	`, paymentID).Scan(&promoID, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, 0, 0, nil
	}
	if err != nil {
		return false, 0, 0, fmt.Errorf("mark used: %w", err)
	}
	return true, promoID, userID, nil
}

// UserLookup — результат поиска пользователя по username или telegram_id.
// Используется PromoAPI.LookupUser для разрешения /promo @username в user.id.
type UserLookup struct {
	ID         int64
	TelegramID int64
	Username   string
	FirstName  string
}

// LookupByUsername находит пользователя по username (без @-префикса).
// Регистронезависимый поиск. ErrPromoNotFound если не найден.
func (r *PromoRepository) LookupByUsername(ctx context.Context, username string) (*UserLookup, error) {
	var u UserLookup
	err := r.db.QueryRow(ctx, `
		SELECT id, telegram_id, COALESCE(username, ''), COALESCE(first_name, '')
		FROM users
		WHERE username ILIKE $1
		LIMIT 1
	`, username).Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPromoNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup by username: %w", err)
	}
	return &u, nil
}

// LookupByTelegramID находит пользователя по telegram_id.
// ErrPromoNotFound если не найден.
func (r *PromoRepository) LookupByTelegramID(ctx context.Context, telegramID int64) (*UserLookup, error) {
	var u UserLookup
	err := r.db.QueryRow(ctx, `
		SELECT id, telegram_id, COALESCE(username, ''), COALESCE(first_name, '')
		FROM users
		WHERE telegram_id = $1
	`, telegramID).Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPromoNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup by telegram_id: %w", err)
	}
	return &u, nil
}

// LookupOrCreateByTelegramID — как LookupByTelegramID, но при отсутствии
// в users пробует найти юзера в bot_starts (нажал /start, но не открыл
// Mini App) и создаёт «shadow»-запись в users с данными из bot_starts.
//
// Используется админ-командой /promo, чтобы можно было выдать промо
// тем юзерам, которые ещё не открывали Mini App (т.е. отсутствуют в
// users.id, но известны нам по telegram_id из воронки бота).
//
// Если bot_starts.campaign_id IS NOT NULL — попутно вставляем
// user_attribution (first-touch). Это сохраняет атрибуцию кампании,
// которая иначе бы потерялась (ValidateTelegramUser ставит её только
// для isNewUser, а тут юзер при позднем заходе уже будет existing).
//
// Реферальная атрибуция (start_param=ref_*) не переносится — для этого
// нужен вызов в referral-service, что усложняет код. Задокументировано
// как известное ограничение.
//
// Возвращает (user, created, err). created=true если создали shadow-юзера;
// false если юзер уже был в users. ErrPromoNotFound если ни в users, ни
// в bot_starts его нет.
func (r *PromoRepository) LookupOrCreateByTelegramID(
	ctx context.Context, telegramID int64,
) (*UserLookup, bool, error) {
	// Быстрый путь: уже есть в users.
	u, err := r.LookupByTelegramID(ctx, telegramID)
	if err == nil {
		return u, false, nil
	}
	if !errors.Is(err, ErrPromoNotFound) {
		return nil, false, err
	}

	// Юзера нет → пробуем bot_starts. Всё в одной транзакции, чтобы
	// гонка с параллельным ValidateTelegramUser не привела к
	// потере attribution или к ErrNoRows на INSERT.
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var bs struct {
		Username   string
		FirstName  string
		StartParam string
		CampaignID *int64
	}
	err = tx.QueryRow(ctx, `
		SELECT username, first_name, start_param, campaign_id
		FROM bot_starts
		WHERE telegram_id = $1
	`, telegramID).Scan(&bs.Username, &bs.FirstName, &bs.StartParam, &bs.CampaignID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrPromoNotFound
	}
	if err != nil {
		return nil, false, fmt.Errorf("lookup bot_starts: %w", err)
	}

	// Insert shadow-юзера. ON CONFLICT DO UPDATE используем как «upsert
	// по telegram_id», но для нашего случая (users только что не было)
	// конфликт может случиться только при гонке с ValidateTelegramUser —
	// возвращаем существующую запись.
	out := &UserLookup{TelegramID: telegramID}
	err = tx.QueryRow(ctx, `
		INSERT INTO users (telegram_id, username, first_name)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''))
		ON CONFLICT (telegram_id) DO UPDATE
		   SET telegram_id = EXCLUDED.telegram_id  -- no-op для возврата id
		RETURNING id, telegram_id, COALESCE(username, ''), COALESCE(first_name, '')
	`, telegramID, bs.Username, bs.FirstName).
		Scan(&out.ID, &out.TelegramID, &out.Username, &out.FirstName)
	if err != nil {
		return nil, false, fmt.Errorf("create shadow user: %w", err)
	}

	// Backfill campaign attribution, если было.
	if bs.CampaignID != nil && *bs.CampaignID != 0 {
		_, err = tx.Exec(ctx, `
			INSERT INTO user_attribution (user_id, campaign_id)
			VALUES ($1, $2)
			ON CONFLICT (user_id) DO NOTHING
		`, out.ID, *bs.CampaignID)
		if err != nil {
			return nil, false, fmt.Errorf("insert user_attribution: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit tx: %w", err)
	}
	return out, true, nil
}

// LookupOrCreateByUsername — то же что LookupByUsername, но при
// отсутствии в users пробует bot_starts (по полю bot_starts.username,
// которое фиксируется на момент /start). См. LookupOrCreateByTelegramID.
//
// Внимание: bot_starts.username — снимок на момент /start, юзер мог
// сменить username в Telegram. Для рассылки 95 bounced юзерам
// предпочтительнее идти через telegram_id (см. broadcast-скрипт).
func (r *PromoRepository) LookupOrCreateByUsername(
	ctx context.Context, username string,
) (*UserLookup, bool, error) {
	u, err := r.LookupByUsername(ctx, username)
	if err == nil {
		return u, false, nil
	}
	if !errors.Is(err, ErrPromoNotFound) {
		return nil, false, err
	}

	// Найти telegram_id в bot_starts по username и делегировать.
	var telegramID int64
	err = r.db.QueryRow(ctx, `
		SELECT telegram_id
		FROM bot_starts
		WHERE username ILIKE $1
		LIMIT 1
	`, username).Scan(&telegramID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrPromoNotFound
	}
	if err != nil {
		return nil, false, fmt.Errorf("lookup bot_starts by username: %w", err)
	}
	return r.LookupOrCreateByTelegramID(ctx, telegramID)
}

// IsExpired — для удобства логики в service-слое.
func (c *PromoCode) IsExpired() bool {
	return c.ExpiresAt != nil && time.Now().After(*c.ExpiresAt)
}

func (c *PromoCode) IsUsed() bool {
	return c.UsedAt != nil
}

// generateToken — 32 байта crypto/rand → 64 hex-символа.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// isUniqueViolation проверяет постгресовую ошибку 23505. Дешёвый
// substring-матч достаточен для in-process retry-логики (не делаем
// pgconn.PgError type-assertion ради простоты).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key value")
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
