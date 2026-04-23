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
	Telegram TelegramConfig
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

// ServicesConfig — gRPC-адреса соседей, которые мы дёргаем при
// successful_payment / refund.
type ServicesConfig struct {
	SubscriptionAddr string
	VPNAddr          string
}

// TelegramConfig — для вызова Bot API (createInvoiceLink, answerPreCheckoutQuery).
type TelegramConfig struct {
	BotToken string
	// WebhookSecret — значение `secret_token` из setWebhook. Gateway валидирует
	// header X-Telegram-Bot-Api-Secret-Token; сюда прокидывается для sanity-check.
	WebhookSecret string
}

type LogConfig struct {
	Level string
}

func New() (*Config, error) {
	grpcPort, err := strconv.Atoi(getEnv("GRPC_PORT", "50063"))
	if err != nil {
		return nil, fmt.Errorf("invalid GRPC_PORT: %w", err)
	}
	dbPort, err := strconv.Atoi(getEnv("DB_PORT", "5432"))
	if err != nil {
		return nil, fmt.Errorf("invalid DB_PORT: %w", err)
	}

	return &Config{
		GRPC: GRPCConfig{
			Host: getEnv("GRPC_HOST", "0.0.0.0"),
			Port: grpcPort,
		},
		DB: DBConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     dbPort,
			User:     getEnv("DB_USER", "vpn"),
			Password: getEnv("DB_PASSWORD", ""),
			Database: getEnv("DB_NAME", "vpn"),
		},
		Services: ServicesConfig{
			SubscriptionAddr: getEnv("SUBSCRIPTION_SERVICE_ADDR", "localhost:50061"),
			VPNAddr:          getEnv("VPN_SERVICE_ADDR", "localhost:50062"),
		},
		Telegram: TelegramConfig{
			BotToken:      getEnv("TELEGRAM_BOT_TOKEN", ""),
			WebhookSecret: getEnv("TELEGRAM_WEBHOOK_SECRET", ""),
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
	if c.Telegram.BotToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
