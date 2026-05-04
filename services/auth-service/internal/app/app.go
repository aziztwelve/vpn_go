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
	grpchealth "github.com/vpn/platform/pkg/grpc/health"
	"github.com/vpn/platform/pkg/telegram"
	pb "github.com/vpn/shared/pkg/proto/auth/v1"
	referralpb "github.com/vpn/shared/pkg/proto/referral/v1"
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

	referralConn *grpc.ClientConn // nil если REFERRAL_SERVICE_ADDR не задан

	// retentionCron — ежедневный генератор retention-drafts. nil если
	// RETENTION_CRON_ENABLED=false. Run запускается в Start() в goroutine,
	// останавливается через closer (cancellation context).
	retentionCron *service.RetentionCron
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

	// Инициализируем retention cron (если включён). Запуск — в Start().
	if err := app.initRetentionCron(); err != nil {
		return nil, fmt.Errorf("failed to init retention cron: %w", err)
	}

	return app, nil
}

// initRetentionCron конструирует RetentionCron если RETENTION_CRON_ENABLED=true.
// Ошибки конструктора здесь невозможны (New не возвращает err), но форм-метод
// оставлен чтобы проще было добавить валидацию RunAtUTC / MiniAppURL позже.
func (a *App) initRetentionCron() error {
	if !a.config.Retention.Enabled {
		a.logger.Info("Retention cron disabled (RETENTION_CRON_ENABLED=false)")
		return nil
	}
	tgClient := telegram.New(a.config.Telegram.BotToken)
	broadcastRepo := repository.NewBroadcastRepository(a.db)
	a.retentionCron = service.NewRetentionCron(
		broadcastRepo,
		tgClient,
		service.RetentionCronConfig{
			Enabled:         a.config.Retention.Enabled,
			RunAtUTC:        a.config.Retention.RunAtUTC,
			MiniAppURL:      a.config.Retention.MiniAppURL,
			SupportUsername: a.config.Retention.SupportUsername,
		},
		a.logger,
	)
	a.logger.Info("Retention cron initialized",
		zap.String("run_at_utc", a.config.Retention.RunAtUTC))
	return nil
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

	// Опциональный клиент к referral-service. Если REFERRAL_SERVICE_ADDR
	// не задан — auth работает без реферальной интеграции.
	var refClient service.ReferralClient
	if a.config.Services.ReferralAddr != "" {
		conn, err := grpc.NewClient(a.config.Services.ReferralAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("dial referral-service: %w", err)
		}
		a.referralConn = conn
		a.closer.Add(func(ctx context.Context) error { return a.referralConn.Close() })
		refClient = referralpb.NewReferralServiceClient(conn)
		a.logger.Info("Referral client configured",
			zap.String("addr", a.config.Services.ReferralAddr),
		)
	}

	// Create services
	authService := service.NewAuthService(
		userRepo,
		a.config.JWT.Secret,
		a.config.JWT.TTLHours,
		a.config.Telegram.BotToken,
		refClient,
		a.logger,
	)

	// Create API
	authAPI := api.NewAuthAPI(authService, a.logger)

	// Create gRPC server
	a.grpcServer = grpc.NewServer()
	pb.RegisterAuthServiceServer(a.grpcServer, authAPI)
	reflection.Register(a.grpcServer)

	// gRPC Health (gRPC Health v1) — пинг БД для readiness-проверки.
	// Используется агрегатором /ready в Gateway.
	grpchealth.RegisterServiceWithChecks(a.grpcServer, func(ctx context.Context) error {
		return a.db.Ping(ctx)
	})

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

	// Запускаем retention cron в фоне. cancellation через отдельный контекст,
	// зарегистрированный в closer'е — при Stop() context.Done() триггернётся
	// и горутина корректно выйдет из Run().
	if a.retentionCron != nil {
		cronCtx, cancel := context.WithCancel(context.Background())
		a.closer.Add(func(ctx context.Context) error {
			cancel()
			return nil
		})
		go a.retentionCron.Run(cronCtx)
	}

	return nil
}

func (a *App) Stop(ctx context.Context) error {
	a.logger.Info("Stopping Auth Service...")
	return a.closer.CloseAll(ctx)
}
