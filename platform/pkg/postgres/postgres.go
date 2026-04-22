package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultMaxPoolSize  = 10
	defaultConnAttempts = 5
	defaultConnTimeout  = time.Second
)

// Config содержит настройки подключения к PostgreSQL
type Config struct {
	Host     string
	Port     string
	Database string
	Schema   string
	User     string
	Password string

	MaxPoolSize  int
	ConnAttempts int
	ConnTimeout  time.Duration
}

// NewPool создает новый connection pool для PostgreSQL
func NewPool(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	if cfg.MaxPoolSize == 0 {
		cfg.MaxPoolSize = defaultMaxPoolSize
	}
	if cfg.ConnAttempts == 0 {
		cfg.ConnAttempts = defaultConnAttempts
	}
	if cfg.ConnTimeout == 0 {
		cfg.ConnTimeout = defaultConnTimeout
	}

	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable&search_path=%s",
		cfg.User,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		cfg.Database,
		cfg.Schema,
	)

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse pool config: %w", err)
	}

	poolConfig.MaxConns = int32(cfg.MaxPoolSize)

	var pool *pgxpool.Pool
	for attempt := 1; attempt <= cfg.ConnAttempts; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
		if err == nil {
			break
		}

		if attempt < cfg.ConnAttempts {
			time.Sleep(cfg.ConnTimeout)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to postgres after %d attempts: %w", cfg.ConnAttempts, err)
	}

	// Проверяем соединение
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping postgres: %w", err)
	}

	return pool, nil
}
