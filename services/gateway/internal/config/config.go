package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	HTTP     HTTPConfig
	Services ServicesConfig
	JWT      JWTConfig
	Log      LogConfig
}

// JWTConfig — секрет общий с Auth Service (тот же HMAC-ключ).
type JWTConfig struct {
	Secret string
}

type HTTPConfig struct {
	Host string
	Port int
}

type ServicesConfig struct {
	AuthAddr         string
	SubscriptionAddr string
	VPNAddr          string
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
		},
		JWT: JWTConfig{
			Secret: getEnv("JWT_SECRET", ""),
		},
		Log: LogConfig{
			Level: getEnv("LOG_LEVEL", "info"),
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
