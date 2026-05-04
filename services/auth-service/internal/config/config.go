package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	GRPC      GRPCConfig
	DB        DBConfig
	JWT       JWTConfig
	Telegram  TelegramConfig
	Services  ServicesConfig
	Retention RetentionConfig
	Log       LogConfig
}

// ServicesConfig — внешние gRPC сервисы.
// ReferralAddr может быть пустым — тогда auth не дёргает referral на новых юзерах.
type ServicesConfig struct {
	ReferralAddr string
}

type GRPCConfig struct {
	Host string
	Port int
}

type DBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

type JWTConfig struct {
	Secret  string
	TTLHours int
}

type TelegramConfig struct {
	BotToken string
}

// RetentionConfig — параметры RetentionCron'а.
// Enabled=false полностью выключает крон (для dev/test или если мы хотим
// временно прервать рассылки). MiniAppURL и SupportUsername подставляются
// в ссылки inline-кнопок превью и (когда Stage 3 приедет) в шаблоны
// сообщений юзерам.
type RetentionConfig struct {
	Enabled         bool
	RunAtUTC        string // "HH:MM", default "14:00" = 17:00 МСК
	MiniAppURL      string // https://app.maydavpn.com или аналог
	SupportUsername string // без @
}

type LogConfig struct {
	Level string
}

func New() (*Config, error) {
	grpcPort, err := strconv.Atoi(getEnv("GRPC_PORT", "50060"))
	if err != nil {
		return nil, fmt.Errorf("invalid GRPC_PORT: %w", err)
	}

	dbPort, err := strconv.Atoi(getEnv("DB_PORT", "5432"))
	if err != nil {
		return nil, fmt.Errorf("invalid DB_PORT: %w", err)
	}

	jwtTTL, err := strconv.Atoi(getEnv("JWT_TTL_HOURS", "168"))
	if err != nil {
		return nil, fmt.Errorf("invalid JWT_TTL_HOURS: %w", err)
	}

	return &Config{
		GRPC: GRPCConfig{
			Host: getEnv("GRPC_HOST", "0.0.0.0"),
			Port: grpcPort,
		},
		DB: DBConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     dbPort,
			User:     getEnv("DB_USER", "admin"),
			Password: getEnv("DB_PASSWORD", ""),
			Database: getEnv("DB_NAME", "vpn"),
		},
		JWT: JWTConfig{
			Secret:   getEnv("JWT_SECRET", ""),
			TTLHours: jwtTTL,
		},
		Telegram: TelegramConfig{
			BotToken: getEnv("TELEGRAM_BOT_TOKEN", ""),
		},
		Services: ServicesConfig{
			ReferralAddr: getEnv("REFERRAL_SERVICE_ADDR", ""),
		},
		Retention: RetentionConfig{
			Enabled:         getEnvBool("RETENTION_CRON_ENABLED", true),
			RunAtUTC:        getEnv("RETENTION_CRON_AT_UTC", "14:00"),
			MiniAppURL:      getEnv("MINI_APP_URL", "https://app.maydavpn.com"),
			SupportUsername: getEnv("SUPPORT_USERNAME", "maydavpn_support"),
		},
		Log: LogConfig{
			Level: getEnv("LOG_LEVEL", "info"),
		},
	}, nil
}

func (c *Config) Validate() error {
	if c.JWT.Secret == "" {
		return fmt.Errorf("JWT_SECRET is required")
	}
	if c.Telegram.BotToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if c.DB.Password == "" {
		return fmt.Errorf("DB_PASSWORD is required")
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvBool — "true"/"1"/"yes" → true; "false"/"0"/"no" → false; пусто → default.
// Регистронезависимо. Любое другое значение = default (не фейлим, чтобы
// опечатка в env не ломала старт).
func getEnvBool(key string, defaultValue bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	switch v {
	case "true", "TRUE", "True", "1", "yes", "YES", "Yes":
		return true
	case "false", "FALSE", "False", "0", "no", "NO", "No":
		return false
	default:
		return defaultValue
	}
}
