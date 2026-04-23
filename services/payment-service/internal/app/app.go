package app

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vpn/payment-service/internal/api"
	"github.com/vpn/payment-service/internal/config"
	"github.com/vpn/payment-service/internal/repository"
	"github.com/vpn/payment-service/internal/service"
	"github.com/vpn/platform/pkg/closer"
	"github.com/vpn/platform/pkg/telegram"
	pb "github.com/vpn/shared/pkg/proto/payment/v1"
	subpb "github.com/vpn/shared/pkg/proto/subscription/v1"
	vpnpb "github.com/vpn/shared/pkg/proto/vpn/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

type App struct {
	config     *config.Config
	logger     *zap.Logger
	db         *pgxpool.Pool
	grpcServer *grpc.Server
	closer     *closer.Closer

	subConn *grpc.ClientConn
	vpnConn *grpc.ClientConn
}

func New(logger *zap.Logger) (*App, error) {
	cfg, err := config.New()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	app := &App{config: cfg, logger: logger, closer: closer.New()}

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
		return fmt.Errorf("connect db: %w", err)
	}
	if err := db.Ping(context.Background()); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}
	a.db = db
	a.closer.Add(func(ctx context.Context) error { a.db.Close(); return nil })
	a.logger.Info("Database connected")
	return nil
}

func (a *App) initGRPC() error {
	// Подключаемся к соседям.
	subConn, err := grpc.NewClient(a.config.Services.SubscriptionAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial subscription-service: %w", err)
	}
	a.subConn = subConn
	a.closer.Add(func(ctx context.Context) error { return subConn.Close() })

	vpnConn, err := grpc.NewClient(a.config.Services.VPNAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial vpn-service: %w", err)
	}
	a.vpnConn = vpnConn
	a.closer.Add(func(ctx context.Context) error { return vpnConn.Close() })

	// Собираем сервис.
	repo := repository.New(a.db)
	tg := telegram.New(a.config.Telegram.BotToken)
	svc := service.New(
		repo,
		tg,
		subpb.NewSubscriptionServiceClient(subConn),
		vpnpb.NewVPNServiceClient(vpnConn),
		a.config.Telegram.MiniAppURL,
		a.logger,
	)
	paymentAPI := api.New(svc, a.logger)

	a.grpcServer = grpc.NewServer()
	pb.RegisterPaymentServiceServer(a.grpcServer, paymentAPI)
	reflection.Register(a.grpcServer)

	a.closer.Add(func(ctx context.Context) error {
		stopped := make(chan struct{})
		go func() { a.grpcServer.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			a.grpcServer.Stop()
		}
		return nil
	})

	a.logger.Info("gRPC clients initialized",
		zap.String("subscription", a.config.Services.SubscriptionAddr),
		zap.String("vpn", a.config.Services.VPNAddr),
	)
	return nil
}

func (a *App) Start() error {
	addr := fmt.Sprintf("%s:%d", a.config.GRPC.Host, a.config.GRPC.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
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
	a.logger.Info("Stopping Payment Service...")
	return a.closer.CloseAll(ctx)
}
