package app

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vpn/platform/pkg/closer"
	"github.com/vpn/platform/pkg/xray"
	"github.com/vpn/vpn-service/internal/api"
	"github.com/vpn/vpn-service/internal/config"
	"github.com/vpn/vpn-service/internal/model"
	"github.com/vpn/vpn-service/internal/repository"
	"github.com/vpn/vpn-service/internal/service"
	pb "github.com/vpn/shared/pkg/proto/vpn/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

type App struct {
	config     *config.Config
	logger     *zap.Logger
	db         *pgxpool.Pool
	xray       *xray.Client
	repo       *repository.VPNRepository
	svc        *service.VPNService
	heartbeat  *service.Heartbeat
	loadCron   *service.LoadCron
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

	if err := app.seedLocalServer(context.Background()); err != nil {
		return nil, err
	}

	if err := app.initXray(); err != nil {
		return nil, err
	}

	if err := app.initGRPC(); err != nil {
		return nil, err
	}

	return app, nil
}

func (a *App) initXray() error {
	addr := fmt.Sprintf("%s:%d", a.config.Xray.APIHost, a.config.Xray.APIPort)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cli, err := xray.New(ctx, addr)
	if err != nil {
		return fmt.Errorf("failed to connect to xray api at %s: %w", addr, err)
	}
	a.xray = cli
	a.closer.Add(func(ctx context.Context) error { return cli.Close() })
	a.logger.Info("Xray API connected", zap.String("addr", addr))
	return nil
}

// seedLocalServer — upsert-ит локальный Xray-сервер в vpn_servers при
// каждом старте. Ключи / short_id / public host берутся из env (Xray config),
// чтобы БД и config.json Xray всегда оставались синхронными.
func (a *App) seedLocalServer(ctx context.Context) error {
	if a.config.Xray.RealityPublicKey == "" || a.config.Xray.RealityShortID == "" {
		a.logger.Warn("skipping local server seed: XRAY_REALITY_* not configured")
		return nil
	}

	repo := repository.NewVPNRepository(a.db)
	seeded, err := repo.UpsertServerByName(ctx, &model.VPNServer{
		Name:        "Local Xray (dev)",
		Location:    "Localhost",
		CountryCode: "XX",
		Host:        a.config.Xray.PublicHost,
		Port:        int32(a.config.Xray.VLESSPort),
		PublicKey:   a.config.Xray.RealityPublicKey,
		// PrivateKey в БД не храним для dev-сервера (он у Xray в config.json).
		// Используем пустую строку — на проде приватник будет заполнен только для
		// serverов, которыми управляет наш VPN Service.
		PrivateKey:  "",
		ShortID:     a.config.Xray.RealityShortID,
		Dest:        a.config.Xray.RealitySNI + ":443",
		ServerNames: a.config.Xray.RealitySNI,
		XrayAPIHost: a.config.Xray.APIHost,
		XrayAPIPort: int32(a.config.Xray.APIPort),
		InboundTag:  a.config.Xray.InboundTag,
		IsActive:    true,
	})
	if err != nil {
		return fmt.Errorf("failed to seed local xray server: %w", err)
	}
	a.logger.Info("Local Xray server seeded",
		zap.Int32("server_id", seeded.ID),
		zap.String("host", seeded.Host),
		zap.Int32("port", seeded.Port),
		zap.String("inbound_tag", seeded.InboundTag),
	)
	return nil
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
	a.repo = repository.NewVPNRepository(a.db)
	a.svc = service.NewVPNService(a.repo, a.xray, a.logger)
	vpnAPI := api.NewVPNAPI(a.svc, a.logger)
	a.heartbeat = service.NewHeartbeat(a.repo, a.xray, a.logger)
	a.loadCron = service.NewLoadCron(a.repo, a.logger)

	a.grpcServer = grpc.NewServer()
	pb.RegisterVPNServiceServer(a.grpcServer, vpnAPI)
	reflection.Register(a.grpcServer)

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

	// Heartbeat: опрос Xray Stats API → обновление last_seen.
	// Ctx отменяется при shutdown → горутина сама корректно завершится.
	hbCtx, hbCancel := context.WithCancel(context.Background())
	a.closer.Add(func(ctx context.Context) error {
		hbCancel()
		return nil
	})
	go a.heartbeat.Run(hbCtx)

	// LoadCron: пересчёт vpn_servers.load_percent каждые 60с.
	loadCtx, loadCancel := context.WithCancel(context.Background())
	a.closer.Add(func(ctx context.Context) error {
		loadCancel()
		return nil
	})
	go a.loadCron.Run(loadCtx)

	// Resync-on-startup: Xray держит user list in-memory, после рестарта
	// контейнера список пустой. Прогоняем ResyncServer по всем активным
	// серверам — они получают обратно всех UUID из Postgres.
	// Делаем в фоне, чтобы gRPC-сервер успел подняться (здоровье важнее
	// чем полный resync); ошибки не роняют сервис — можно дёрнуть ручкой.
	go a.resyncAllServersOnStartup()

	return nil
}

func (a *App) resyncAllServersOnStartup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	servers, err := a.repo.ListServers(ctx, true)
	if err != nil {
		a.logger.Warn("startup resync: failed to list active servers", zap.Error(err))
		return
	}
	if len(servers) == 0 {
		a.logger.Info("startup resync: no active servers, skipping")
		return
	}

	a.logger.Info("startup resync: starting", zap.Int("servers", len(servers)))
	for _, srv := range servers {
		res, err := a.svc.ResyncServer(ctx, srv.ID)
		if err != nil {
			a.logger.Warn("startup resync: server failed",
				zap.Int32("server_id", srv.ID),
				zap.String("name", srv.Name),
				zap.Error(err))
			continue
		}
		a.logger.Info("startup resync: server done",
			zap.Int32("server_id", srv.ID),
			zap.String("name", srv.Name),
			zap.Int32("total", res.Total),
			zap.Int32("added", res.Added),
			zap.Int32("already", res.AlreadyExist),
			zap.Int32("failed", res.Failed),
		)
	}
}

func (a *App) Stop(ctx context.Context) error {
	a.logger.Info("Stopping VPN Service...")
	return a.closer.CloseAll(ctx)
}
