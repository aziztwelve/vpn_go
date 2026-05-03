package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vpn/auth-service/internal/model"
)

type UserRepository struct {
	db *pgxpool.Pool
}

func NewUserRepository(db *pgxpool.Pool) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) CreateUser(ctx context.Context, telegramID int64, username, firstName, lastName, photoURL, langCode string) (*model.User, error) {
	query := `
		INSERT INTO users (telegram_id, username, first_name, last_name, photo_url, language_code)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, telegram_id, username, first_name, last_name, photo_url, language_code, role, is_banned, balance, created_at, updated_at, last_active_at
	`

	user := &model.User{}
	err := r.db.QueryRow(ctx, query, telegramID, username, firstName, lastName, photoURL, langCode).Scan(
		&user.ID,
		&user.TelegramID,
		&user.Username,
		&user.FirstName,
		&user.LastName,
		&user.PhotoURL,
		&user.LanguageCode,
		&user.Role,
		&user.IsBanned,
		&user.Balance,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.LastActiveAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	return user, nil
}

func (r *UserRepository) GetUserByTelegramID(ctx context.Context, telegramID int64) (*model.User, error) {
	query := `
		SELECT id, telegram_id, username, first_name, last_name, photo_url, language_code, role, is_banned, balance, created_at, updated_at, last_active_at
		FROM users
		WHERE telegram_id = $1
	`

	user := &model.User{}
	err := r.db.QueryRow(ctx, query, telegramID).Scan(
		&user.ID,
		&user.TelegramID,
		&user.Username,
		&user.FirstName,
		&user.LastName,
		&user.PhotoURL,
		&user.LanguageCode,
		&user.Role,
		&user.IsBanned,
		&user.Balance,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.LastActiveAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by telegram_id: %w", err)
	}

	return user, nil
}

func (r *UserRepository) GetUserByID(ctx context.Context, userID int64) (*model.User, error) {
	query := `
		SELECT id, telegram_id, username, first_name, last_name, photo_url, language_code, role, is_banned, balance, created_at, updated_at, last_active_at
		FROM users
		WHERE id = $1
	`

	user := &model.User{}
	err := r.db.QueryRow(ctx, query, userID).Scan(
		&user.ID,
		&user.TelegramID,
		&user.Username,
		&user.FirstName,
		&user.LastName,
		&user.PhotoURL,
		&user.LanguageCode,
		&user.Role,
		&user.IsBanned,
		&user.Balance,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.LastActiveAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by id: %w", err)
	}

	return user, nil
}

func (r *UserRepository) UpdateUser(ctx context.Context, user *model.User) error {
	query := `
		UPDATE users
		SET username = $1, first_name = $2, last_name = $3, photo_url = $4, language_code = $5, updated_at = NOW()
		WHERE id = $6
	`

	_, err := r.db.Exec(ctx, query, user.Username, user.FirstName, user.LastName, user.PhotoURL, user.LanguageCode, user.ID)
	if err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}

	return nil
}

func (r *UserRepository) UpdateUserRole(ctx context.Context, userID int64, role string) (*model.User, error) {
	query := `
		UPDATE users
		SET role = $1, updated_at = NOW()
		WHERE id = $2
		RETURNING id, telegram_id, username, first_name, last_name, photo_url, language_code, role, is_banned, balance, created_at, updated_at, last_active_at
	`

	user := &model.User{}
	err := r.db.QueryRow(ctx, query, role, userID).Scan(
		&user.ID,
		&user.TelegramID,
		&user.Username,
		&user.FirstName,
		&user.LastName,
		&user.PhotoURL,
		&user.LanguageCode,
		&user.Role,
		&user.IsBanned,
		&user.Balance,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.LastActiveAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update user role: %w", err)
	}

	return user, nil
}

func (r *UserRepository) BanUser(ctx context.Context, userID int64, isBanned bool) (*model.User, error) {
	query := `
		UPDATE users
		SET is_banned = $1, updated_at = NOW()
		WHERE id = $2
		RETURNING id, telegram_id, username, first_name, last_name, photo_url, language_code, role, is_banned, balance, created_at, updated_at, last_active_at
	`

	user := &model.User{}
	err := r.db.QueryRow(ctx, query, isBanned, userID).Scan(
		&user.ID,
		&user.TelegramID,
		&user.Username,
		&user.FirstName,
		&user.LastName,
		&user.PhotoURL,
		&user.LanguageCode,
		&user.Role,
		&user.IsBanned,
		&user.Balance,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.LastActiveAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to ban user: %w", err)
	}

	return user, nil
}

func (r *UserRepository) UpdateLastActive(ctx context.Context, userID int64) error {
	query := `UPDATE users SET last_active_at = NOW() WHERE id = $1`
	_, err := r.db.Exec(ctx, query, userID)
	return err
}

// UpsertPendingReferral сохраняет/перезаписывает pending реферальный токен
// для telegram_id. Один telegram_id = один токен (последний clicker побеждает).
func (r *UserRepository) UpsertPendingReferral(ctx context.Context, telegramID int64, refToken string) error {
	query := `
		INSERT INTO pending_referrals (telegram_id, ref_token)
		VALUES ($1, $2)
		ON CONFLICT (telegram_id) DO UPDATE
		SET ref_token = EXCLUDED.ref_token, created_at = NOW()
	`
	_, err := r.db.Exec(ctx, query, telegramID, refToken)
	if err != nil {
		return fmt.Errorf("upsert pending referral: %w", err)
	}
	return nil
}

// PopPendingReferral возвращает токен по telegram_id и удаляет запись (атомарно).
// Если записи нет — возвращает ("", false, nil), без ошибки.
func (r *UserRepository) PopPendingReferral(ctx context.Context, telegramID int64) (string, bool, error) {
	query := `DELETE FROM pending_referrals WHERE telegram_id = $1 RETURNING ref_token`
	var refToken string
	err := r.db.QueryRow(ctx, query, telegramID).Scan(&refToken)
	if err != nil {
		// DELETE ... RETURNING + .Scan возвращает pgx.ErrNoRows если строк не было.
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("pop pending referral: %w", err)
	}
	return refToken, true, nil
}

// InsertBotStart фиксирует первое нажатие /start (для воронки бот → Mini App).
// ON CONFLICT DO NOTHING — повторные /start от того же telegram_id не сдвигают
// started_at. Возвращает stored=true только если запись действительно вставилась
// (первое нажатие).
//
// campaignID опционально (0 = не атрибутировать к кампании). Резолв slug→id
// делается выше в сервисе (см. ResolveCampaignBySlug).
func (r *UserRepository) InsertBotStart(ctx context.Context, telegramID int64, username, firstName, startParam string, campaignID int64) (bool, error) {
	const q = `
		INSERT INTO bot_starts (telegram_id, username, first_name, start_param, campaign_id)
		VALUES ($1, $2, $3, $4, NULLIF($5, 0))
		ON CONFLICT (telegram_id) DO NOTHING
		RETURNING telegram_id
	`
	var inserted int64
	err := r.db.QueryRow(ctx, q, telegramID, username, firstName, startParam, campaignID).Scan(&inserted)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // дубликат — это норма
		}
		return false, fmt.Errorf("insert bot_start: %w", err)
	}
	return true, nil
}

// ResolveCampaignBySlug возвращает id активной кампании по slug'у.
// Возвращает 0, false если slug не найден или кампания архивирована.
// Используется в InsertBotStart и SetPendingCampaign.
func (r *UserRepository) ResolveCampaignBySlug(ctx context.Context, slug string) (int64, bool, error) {
	const q = `SELECT id FROM campaigns WHERE slug = $1 AND is_active = TRUE`
	var id int64
	err := r.db.QueryRow(ctx, q, slug).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("resolve campaign slug: %w", err)
	}
	return id, true, nil
}

// UpsertPendingCampaign сохраняет/перезаписывает pending атрибуцию к
// маркетинговой кампании по telegram_id (аналог UpsertPendingReferral).
// Один telegram_id = одна кампания (последний clicker побеждает).
func (r *UserRepository) UpsertPendingCampaign(ctx context.Context, telegramID, campaignID int64) error {
	const q = `
		INSERT INTO pending_campaigns (telegram_id, campaign_id)
		VALUES ($1, $2)
		ON CONFLICT (telegram_id) DO UPDATE
		SET campaign_id = EXCLUDED.campaign_id, created_at = NOW()
	`
	_, err := r.db.Exec(ctx, q, telegramID, campaignID)
	if err != nil {
		return fmt.Errorf("upsert pending campaign: %w", err)
	}
	return nil
}

// PopPendingCampaign возвращает campaign_id по telegram_id и удаляет запись
// (атомарно). Если записи нет — возвращает (0, false, nil).
func (r *UserRepository) PopPendingCampaign(ctx context.Context, telegramID int64) (int64, bool, error) {
	const q = `DELETE FROM pending_campaigns WHERE telegram_id = $1 RETURNING campaign_id`
	var campaignID int64
	err := r.db.QueryRow(ctx, q, telegramID).Scan(&campaignID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("pop pending campaign: %w", err)
	}
	return campaignID, true, nil
}

// InsertUserAttribution создаёт запись о first-touch атрибуции юзера к кампании.
// Идемпотентно: если запись уже есть (юзер ранее атрибутирован) — не перезаписывает,
// возвращает stored=false. Это гарантирует что первое касание навсегда.
func (r *UserRepository) InsertUserAttribution(ctx context.Context, userID, campaignID int64) (bool, error) {
	const q = `
		INSERT INTO user_attribution (user_id, campaign_id)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO NOTHING
		RETURNING user_id
	`
	var inserted int64
	err := r.db.QueryRow(ctx, q, userID, campaignID).Scan(&inserted)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("insert user attribution: %w", err)
	}
	return true, nil
}

// MarkBotStartAppOpened помечает что юзер открыл Mini App. Если записи /start
// не было (юзер открыл Mini App напрямую без захода в бота) — создаём её
// сразу с opened_app_at=NOW(), started_at тоже NOW(): такие "прямые" открытия
// тоже учитываем в воронке как 100%-конверсию.
//
// Идемпотентно: если opened_app_at уже стоит — не перезаписываем.
func (r *UserRepository) MarkBotStartAppOpened(ctx context.Context, telegramID int64, username, firstName string) error {
	const q = `
		INSERT INTO bot_starts (telegram_id, username, first_name, opened_app_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (telegram_id) DO UPDATE
		SET opened_app_at = COALESCE(bot_starts.opened_app_at, NOW())
	`
	_, err := r.db.Exec(ctx, q, telegramID, username, firstName)
	if err != nil {
		return fmt.Errorf("mark bot_start app opened: %w", err)
	}
	return nil
}
