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
func (r *UserRepository) InsertBotStart(ctx context.Context, telegramID int64, username, firstName, startParam string) (bool, error) {
	const q = `
		INSERT INTO bot_starts (telegram_id, username, first_name, start_param)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (telegram_id) DO NOTHING
		RETURNING telegram_id
	`
	var inserted int64
	err := r.db.QueryRow(ctx, q, telegramID, username, firstName, startParam).Scan(&inserted)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // дубликат — это норма
		}
		return false, fmt.Errorf("insert bot_start: %w", err)
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
