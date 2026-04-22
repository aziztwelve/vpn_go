package app

import (
	"context"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vpn/auth-service/internal/api"
	"github.com/vpn/auth-service/internal/config"
	"github.com/vpn/auth-service/internal/repository"
	"github.com/vpn/auth-service/internal/service"
	"github.com/vpn/platform/pkg/closer"
	pb "github.com/vpn/shared/pkg/proto/auth/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type App struct {
	config     *config.Config
	logger     *zap.Logger
	db         *pgxpool.Pool
	grpcServer *grpc.Server
	closer     *closer.Closer
}

func New(logger *zap.Logger) (*App, error) {
	cfg, err := config.New()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	app := &App{
		config: cfg,
		logger: logger,
		closer: closer.New(),
	}

	// Initialize database
	if err := app.initDB(); err != nil {
		return nil, fmt.Errorf("failed to init database: %w", err)
	}

	// Initialize gRPC server
	if err := app.initGRPC(); err != nil {
		return nil, fmt.Errorf("failed to init gRPC: %w", err)
	}

	return app, nil
}

func (a *App) initDB() error {
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		a.config.DB.User,
		a.config.DB.Password,
		a.config.DB.Host,
		a.config.DB.Port,
		a.config.DB.Database,
	)

	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := db.Ping(context.Background()); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	a.db = db
	a.closer.Add(func(ctx context.Context) error {
		a.db.Close()
		return nil
	})

	a.logger.Info("Database connected")
	return nil
}

func (a *App) initGRPC() error {
	// Create repositories
	userRepo := repository.NewUserRepository(a.db)

	// Create services
	authService := service.NewAuthService(
		userRepo,
		a.config.JWT.Secret,
		a.config.JWT.TTLHours,
		a.config.Telegram.BotToken,
		a.logger,
	)

	// Create API
	authAPI := api.NewAuthAPI(authService, a.logger)

	// Create gRPC server
	a.grpcServer = grpc.NewServer()
	pb.RegisterAuthServiceServer(a.grpcServer, authAPI)

	a.closer.Add(func(ctx context.Context) error {
		a.grpcServer.GracefulStop()
		return nil
	})

	return nil
}

func (a *App) Start() error {
	addr := fmt.Sprintf("%s:%d", a.config.GRPC.Host, a.config.GRPC.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	a.logger.Info("Starting gRPC server", zap.String("addr", addr))

	go func() {
		if err := a.grpcServer.Serve(listener); err != nil {
			a.logger.Fatal("gRPC server error", zap.Error(err))
		}
	}()

	return nil
}

func (a *App) Stop(ctx context.Context) error {
	a.logger.Info("Stopping Auth Service...")
	return a.closer.CloseAll(ctx)
}
