package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	GRPC GRPCConfig
	DB   DBConfig
	Xray XrayConfig
	Log  LogConfig
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

type XrayConfig struct {
	// Адрес gRPC API Xray (dokodemo-door с tag=api).
	APIHost string
	APIPort int
	// Имя VLESS+Reality inbound (AlterInbound → Add/RemoveUser).
	InboundTag string
	// Что показываем клиенту в VLESS-ссылке.
	PublicHost        string
	VLESSPort         int
	RealityPublicKey  string
	RealityShortID    string
	RealitySNI        string
}

type LogConfig struct {
	Level string
}

func New() (*Config, error) {
	grpcPort, err := strconv.Atoi(getEnv("GRPC_PORT", "50062"))
	if err != nil {
		return nil, fmt.Errorf("invalid GRPC_PORT: %w", err)
	}

	dbPort, err := strconv.Atoi(getEnv("DB_PORT", "5432"))
	if err != nil {
		return nil, fmt.Errorf("invalid DB_PORT: %w", err)
	}

	xrayAPIPort, err := strconv.Atoi(getEnv("XRAY_API_PORT", "10085"))
	if err != nil {
		return nil, fmt.Errorf("invalid XRAY_API_PORT: %w", err)
	}

	vlessPort, err := strconv.Atoi(getEnv("XRAY_VLESS_PORT", "443"))
	if err != nil {
		return nil, fmt.Errorf("invalid XRAY_VLESS_PORT: %w", err)
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
		Xray: XrayConfig{
			APIHost:          getEnv("XRAY_API_HOST", "xray"),
			APIPort:          xrayAPIPort,
			InboundTag:       getEnv("XRAY_INBOUND_TAG", "vless-reality-in"),
			PublicHost:       getEnv("XRAY_PUBLIC_HOST", "localhost"),
			VLESSPort:        vlessPort,
			RealityPublicKey: getEnv("XRAY_REALITY_PUBLIC_KEY", ""),
			RealityShortID:   getEnv("XRAY_REALITY_SHORT_ID", ""),
			RealitySNI:       getEnv("XRAY_REALITY_SNI", "github.com"),
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
