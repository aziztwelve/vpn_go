package repository

import (
	"context"
	"fmt"

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
