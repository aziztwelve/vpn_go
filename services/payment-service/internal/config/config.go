package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	GRPC     GRPCConfig
	DB       DBConfig
	Services ServicesConfig
	Telegram TelegramConfig
	Wata     WataConfig
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

// TelegramConfig — для вызова Bot API (createInvoiceLink, answerPreCheckoutQuery,
// sendMessage при /start и т.п.).
type TelegramConfig struct {
	BotToken string
	// WebhookSecret — значение `secret_token` из setWebhook. Gateway валидирует
	// header X-Telegram-Bot-Api-Secret-Token; сюда прокидывается для sanity-check.
	WebhookSecret string
	// MiniAppURL — https-URL Mini App (prod: https://cdn.osmonai.com). Используется
	// в приветственном сообщении /start и в setChatMenuButton. Если пусто —
	// /start-handler скипнет отправку WebApp-кнопки и покажет только текст.
	MiniAppURL string
}

// WataConfig — настройки провайдера WATA H2H API.
// Если Enabled=false — провайдер не регистрируется в payment-service,
// остальные поля игнорируются.
type WataConfig struct {
	Enabled     bool
	BaseURL     string        // https://api.wata.pro/api/h2h | sandbox
	AccessToken string        // JWT из ЛК merchant.wata.pro
	SuccessURL  string        // куда редиректит после успешной оплаты
	FailURL     string        // куда редиректит после неуспешной
	LinkTTL     time.Duration // время жизни платёжной ссылки (10m…720h)
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
			MiniAppURL:    getEnv("MINIAPP_URL", ""),
		},
		Wata: WataConfig{
			Enabled:     getEnv("WATA_ENABLED", "false") == "true",
			BaseURL:     getEnv("WATA_BASE_URL", "https://api.wata.pro/api/h2h"),
			AccessToken: getEnv("WATA_ACCESS_TOKEN", ""),
			SuccessURL:  getEnv("WATA_SUCCESS_URL", ""),
			FailURL:     getEnv("WATA_FAIL_URL", ""),
			LinkTTL:     parseDurationDefault(getEnv("WATA_LINK_TTL", "72h"), 72*time.Hour),
		},
		Log: LogConfig{
			Level: getEnv("LOG_LEVEL", "info"),
		},
	}, nil
}

// parseDurationDefault парсит Go-duration, при ошибке — дефолт.
// Используется для опциональных настроек (TTL, таймауты), где
// лучше работать с дефолтом, чем падать на старте.
func parseDurationDefault(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return def
}

func (c *Config) Validate() error {
	if c.DB.Password == "" {
		return fmt.Errorf("DB_PASSWORD is required")
	}
	if c.Telegram.BotToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if c.Wata.Enabled {
		if c.Wata.AccessToken == "" {
			return fmt.Errorf("WATA_ENABLED=true but WATA_ACCESS_TOKEN is empty")
		}
		if c.Wata.SuccessURL == "" || c.Wata.FailURL == "" {
			return fmt.Errorf("WATA_ENABLED=true but success/fail redirect URLs are empty")
		}
		if c.Wata.LinkTTL < 10*time.Minute || c.Wata.LinkTTL > 720*time.Hour {
			return fmt.Errorf("WATA_LINK_TTL must be between 10m and 720h, got %s", c.Wata.LinkTTL)
		}
	}
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
