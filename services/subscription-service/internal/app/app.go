package app

import (
	"context"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vpn/platform/pkg/closer"
	"github.com/vpn/subscription-service/internal/api"
	"github.com/vpn/subscription-service/internal/config"
	"github.com/vpn/subscription-service/internal/repository"
	"github.com/vpn/subscription-service/internal/service"
	pb "github.com/vpn/shared/pkg/proto/subscription/v1"
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

	if err := app.initDB(); err != nil {
		return nil, err
	}

	if err := app.initGRPC(); err != nil {
		return nil, err
	}

	return app, nil
}

func (a *App) initDB() error {
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		a.config.DB.User, a.config.DB.Password, a.config.DB.Host, a.config.DB.Port, a.config.DB.Database)

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
	repo := repository.NewSubscriptionRepository(a.db)
	svc := service.NewSubscriptionService(repo, a.logger)
	subscriptionAPI := api.NewSubscriptionAPI(svc, a.logger)

	a.grpcServer = grpc.NewServer()
	pb.RegisterSubscriptionServiceServer(a.grpcServer, subscriptionAPI)

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
	a.logger.Info("Stopping Subscription Service...")
	return a.closer.CloseAll(ctx)
}
