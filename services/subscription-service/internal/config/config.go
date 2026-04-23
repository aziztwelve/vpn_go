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
	Log      LogConfig
}

type ServicesConfig struct {
	VPNAddr string
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

type LogConfig struct {
	Level string
}

func New() (*Config, error) {
	grpcPort, err := strconv.Atoi(getEnv("GRPC_PORT", "50061"))
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
			User:     getEnv("DB_USER", "admin"),
			Password: getEnv("DB_PASSWORD", ""),
			Database: getEnv("DB_NAME", "vpn"),
		},
		Services: ServicesConfig{
			VPNAddr: getEnv("VPN_SERVICE_ADDR", "localhost:50062"),
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
	return nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
