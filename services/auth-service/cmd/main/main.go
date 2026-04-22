package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/vpn/auth-service/internal/app"
	"go.uber.org/zap"
)

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// Initialize logger
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}

	var zapLogger *zap.Logger
	var err error
	if logLevel == "debug" {
		zapLogger, err = zap.NewDevelopment()
	} else {
		zapLogger, err = zap.NewProduction()
	}
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer zapLogger.Sync()

	// Create application
	application, err := app.New(zapLogger)
	if err != nil {
		zapLogger.Fatal("Failed to create application", zap.Error(err))
	}

	// Start application
	if err := application.Start(); err != nil {
		zapLogger.Fatal("Failed to start application", zap.Error(err))
	}

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	zapLogger.Info("Shutting down Auth Service...")

	// Graceful shutdown
	ctx := context.Background()
	if err := application.Stop(ctx); err != nil {
		zapLogger.Error("Error during shutdown", zap.Error(err))
	}

	zapLogger.Info("Auth Service stopped")
}
