package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	HTTP         HTTPConfig
	Services     ServicesConfig
	JWT          JWTConfig
	Telegram     TelegramConfig
	Log          LogConfig
	Subscription SubscriptionConfig
}

// SubscriptionConfig — настройки публичной подписки.
//
// DefaultCountry: ISO-2 код страны (DE/FI/...) для «дефолтного» сервера
// в трёх режимах (⚡ Обычный VPN / 🚀 Обход блокировок / 🎬 YouTube).
// Если в активных есть сервер с таким country_code (case-insensitive)
// — он используется для генерации этих трёх ссылок. Иначе fallback на
// наименее загруженный сервер (servers[0]). Полный список стран в
// подписке НЕ режется — юзер всегда может вручную выбрать другой
// сервер из списка.
type SubscriptionConfig struct {
	DefaultCountry string
}

// JWTConfig — секрет общий с Auth Service (тот же HMAC-ключ).
type JWTConfig struct {
	Secret string
}

// TelegramConfig — shared-секрет для валидации webhook'а от Telegram.
// Должен совпадать с тем что передаётся в setWebhook `secret_token`.
type TelegramConfig struct {
	WebhookSecret   string
	BotToken        string
	ChannelUsername string
}

type HTTPConfig struct {
	Host string
	Port int
}

type ServicesConfig struct {
	AuthAddr         string
	SubscriptionAddr string
	VPNAddr          string
	PaymentAddr      string
}

type LogConfig struct {
	Level string
}

func New() (*Config, error) {
	httpPort, err := strconv.Atoi(getEnv("HTTP_PORT", "8080"))
	if err != nil {
		return nil, fmt.Errorf("invalid HTTP_PORT: %w", err)
	}

	return &Config{
		HTTP: HTTPConfig{
			Host: getEnv("HTTP_HOST", "0.0.0.0"),
			Port: httpPort,
		},
		Services: ServicesConfig{
			AuthAddr:         getEnv("AUTH_SERVICE_ADDR", "localhost:50060"),
			SubscriptionAddr: getEnv("SUBSCRIPTION_SERVICE_ADDR", "localhost:50061"),
			VPNAddr:          getEnv("VPN_SERVICE_ADDR", "localhost:50062"),
			PaymentAddr:      getEnv("PAYMENT_SERVICE_ADDR", "localhost:50063"),
		},
		JWT: JWTConfig{
			Secret: getEnv("JWT_SECRET", ""),
		},
		Telegram: TelegramConfig{
			WebhookSecret:   getEnv("TELEGRAM_WEBHOOK_SECRET", ""),
			BotToken:        getEnv("TELEGRAM_BOT_TOKEN", ""),
			ChannelUsername: getEnv("TELEGRAM_CHANNEL_USERNAME", "@maydavpn"),
		},
		Log: LogConfig{
			Level: getEnv("LOG_LEVEL", "info"),
		},
		Subscription: SubscriptionConfig{
			DefaultCountry: getEnv("SUBSCRIPTION_DEFAULT_COUNTRY", "DE"),
		},
	}, nil
}

// Validate проверяет обязательные поля.
func (c *Config) Validate() error {
	if c.JWT.Secret == "" {
		return fmt.Errorf("JWT_SECRET is required (must match auth-service)")
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
