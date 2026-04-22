package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	HTTP     HTTPConfig
	Services ServicesConfig
	Log      LogConfig
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
		Log: LogConfig{
			Level: getEnv("LOG_LEVEL", "info"),
		},
	}, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
