package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	GRPC     GRPCConfig
	DB       DBConfig
	Services ServicesConfig
	Referral ReferralConfig
	Log      LogConfig
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

// ServicesConfig — gRPC адреса других сервисов.
// SubscriptionAddr нужен чтобы продлевать активную подписку при выдаче
// бонусных дней. AuthAddr — для чтения роли inviter'а (user/partner).
type ServicesConfig struct {
	SubscriptionAddr string
	AuthAddr         string
}

// ReferralConfig — настройки реферальной программы.
type ReferralConfig struct {
	// BotUsername используется для построения deep-link'а:
	//   https://t.me/<BotUsername>?startapp=ref_<token>
	BotUsername string
	// FreshnessSeconds — максимальный возраст приглашённого юзера для
	// валидной регистрации реферала (anti-abuse). По дефолту 60s — приглашённый
	// должен быть только что создан.
	FreshnessSeconds int
	// BonusDays — сколько дней даём за приглашение (обычной модели).
	BonusDays int32
	// PartnerPercent — процент с платежа на partner.balance (0..100).
	PartnerPercent int
	// MinWithdrawalRUB — минимальная сумма для заявки на вывод.
	MinWithdrawalRUB float64
}

type LogConfig struct {
	Level string
}

func New() (*Config, error) {
	grpcPort, err := strconv.Atoi(getEnv("GRPC_PORT", "50064"))
	if err != nil {
		return nil, fmt.Errorf("invalid GRPC_PORT: %w", err)
	}

	dbPort, err := strconv.Atoi(getEnv("DB_PORT", "5432"))
	if err != nil {
		return nil, fmt.Errorf("invalid DB_PORT: %w", err)
	}

	// Внутри контейнера переменные передаются без префикса REFERRAL_
	// (как и у других сервисов: AUTH_GRPC_PORT → GRPC_PORT в auth-service),
	// поэтому здесь читаем "чистые" имена. Префикс REFERRAL_ присутствует
	// только в master deploy/env/.env.
	freshness, err := strconv.Atoi(getEnv("FRESHNESS_SECONDS", "60"))
	if err != nil {
		return nil, fmt.Errorf("invalid FRESHNESS_SECONDS: %w", err)
	}

	bonusDays, err := strconv.Atoi(getEnv("BONUS_DAYS", "3"))
	if err != nil {
		return nil, fmt.Errorf("invalid BONUS_DAYS: %w", err)
	}

	partnerPercent, err := strconv.Atoi(getEnv("PARTNER_PERCENT", "30"))
	if err != nil {
		return nil, fmt.Errorf("invalid PARTNER_PERCENT: %w", err)
	}

	minWithdrawal, err := strconv.ParseFloat(getEnv("MIN_WITHDRAWAL_RUB", "500"), 64)
	if err != nil {
		return nil, fmt.Errorf("invalid MIN_WITHDRAWAL_RUB: %w", err)
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
		Services: ServicesConfig{
			SubscriptionAddr: getEnv("SUBSCRIPTION_SERVICE_ADDR", "localhost:50061"),
			AuthAddr:         getEnv("AUTH_SERVICE_ADDR", "localhost:50060"),
		},
		Referral: ReferralConfig{
			BotUsername:      getEnv("BOT_USERNAME", "maydavpnbot"),
			FreshnessSeconds: freshness,
			BonusDays:        int32(bonusDays),
			PartnerPercent:   partnerPercent,
			MinWithdrawalRUB: minWithdrawal,
		},
		Log: LogConfig{
			Level: getEnv("LOG_LEVEL", "info"),
		},
	}, nil
}

func (c *Config) Validate() error {
	if c.DB.Password == "" {
		return fmt.Errorf("DB_PASSWORD is required")
	}
	if c.Referral.BotUsername == "" {
		return fmt.Errorf("BOT_USERNAME is required")
	}
	if c.Referral.PartnerPercent < 0 || c.Referral.PartnerPercent > 100 {
		return fmt.Errorf("PARTNER_PERCENT must be in [0, 100]")
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
