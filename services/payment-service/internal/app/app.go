package app

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vpn/payment-service/internal/api"
	"github.com/vpn/payment-service/internal/config"
	"github.com/vpn/payment-service/internal/notifier"
	"github.com/vpn/payment-service/internal/provider"
	"github.com/vpn/payment-service/internal/provider/platega"
	"github.com/vpn/payment-service/internal/provider/telegram"
	"github.com/vpn/payment-service/internal/provider/wata"
	"github.com/vpn/payment-service/internal/repository"
	"github.com/vpn/payment-service/internal/sentinel"
	"github.com/vpn/payment-service/internal/service"
	"github.com/vpn/platform/pkg/closer"
	authpb "github.com/vpn/shared/pkg/proto/auth/v1"
	pb "github.com/vpn/shared/pkg/proto/payment/v1"
	referralpb "github.com/vpn/shared/pkg/proto/referral/v1"
	subpb "github.com/vpn/shared/pkg/proto/subscription/v1"
	vpnpb "github.com/vpn/shared/pkg/proto/vpn/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

// App — новая версия приложения с поддержкой нескольких провайдеров.
type App struct {
	config     *config.Config
	logger     *zap.Logger
	db         *pgxpool.Pool
	grpcServer *grpc.Server
	closer     *closer.Closer

	subConn      *grpc.ClientConn
	vpnConn      *grpc.ClientConn
	authConn     *grpc.ClientConn
	referralConn *grpc.ClientConn // nil если REFERRAL_SERVICE_ADDR не задан

	// sentinel — фоновый воркер для добивания зависших платежей.
	// Управляется через sentinelCancel + sentinelWg в Stop().
	sentinel       *sentinel.Sentinel
	sentinelCancel context.CancelFunc
	sentinelWg     sync.WaitGroup
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
	// Подключаемся к соседям
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

	// auth-service — нужен только для конвертации внутреннего user_id в
	// telegram_id перед отправкой push-уведомления о платеже. Один RPC
	// в успешный путь оплаты — допустимая цена за корректность.
	authConn, err := grpc.NewClient(a.config.Services.AuthAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial auth-service: %w", err)
	}
	a.authConn = authConn
	a.closer.Add(func(ctx context.Context) error { return authConn.Close() })

	// referral-service — опциональный. Если адрес не задан, реферальный хук
	// не вызывается, partner-комиссия не начисляется.
	var referralClient service.ReferralClient
	if a.config.Services.ReferralAddr != "" {
		referralConn, err := grpc.NewClient(a.config.Services.ReferralAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("dial referral-service: %w", err)
		}
		a.referralConn = referralConn
		a.closer.Add(func(ctx context.Context) error { return referralConn.Close() })
		referralClient = referralpb.NewReferralServiceClient(referralConn)
		a.logger.Info("Referral client configured",
			zap.String("addr", a.config.Services.ReferralAddr))
	}

	// Инициализируем провайдеры
	providers := a.initProviders()

	// Собираем сервис с провайдерами + Telegram notifier (для push-уведомлений
	// после успешной оплаты). notifier == nil если BotToken пустой —
	// тогда уведомления просто пропускаются.
	tgNotifier := notifier.New(a.config.Telegram.BotToken, a.logger)
	if tgNotifier != nil {
		a.logger.Info("Telegram notifier initialized")
	} else {
		a.logger.Warn("Telegram notifier disabled (BotToken empty)")
	}

	repo := repository.New(a.db)
	svc := service.New(
		repo,
		providers,
		subpb.NewSubscriptionServiceClient(subConn),
		vpnpb.NewVPNServiceClient(vpnConn),
		authpb.NewAuthServiceClient(authConn),
		referralClient,
		tgNotifier,
		a.logger,
	)

	// Sentinel воркер: добивает зависшие платежи (paid_db_only / paid_subscription_done)
	// если webhook handler упал между шагами state machine. Запускается в Start().
	a.sentinel = sentinel.New(repo, svc, sentinel.Config{
		Interval:   60 * time.Second,
		StaleAfter: 5 * time.Minute,
		BatchLimit: 50,
	}, a.logger)

	// Создаём gRPC API
	paymentAPI := api.New(svc, a.logger)

	// Запускаем gRPC сервер
	a.grpcServer = grpc.NewServer()
	pb.RegisterPaymentServiceServer(a.grpcServer, paymentAPI)
	reflection.Register(a.grpcServer)

	addr := fmt.Sprintf("%s:%d", a.config.GRPC.Host, a.config.GRPC.Port)
	a.logger.Info("gRPC server initialized", zap.String("addr", addr))
	return nil
}

func (a *App) initProviders() []provider.PaymentProvider {
	var providers []provider.PaymentProvider

	// Telegram Stars провайдер (опционально, управляется TELEGRAM_STARS_ENABLED).
	// Бот для /start, /bonus и авторизации работает через BotToken независимо от
	// этого флага — здесь гейтится только регистрация Stars как платёжного канала.
	if a.config.Telegram.StarsEnabled {
		if a.config.Telegram.BotToken == "" {
			a.logger.Error("TELEGRAM_STARS_ENABLED=true but TELEGRAM_BOT_TOKEN is empty")
		} else {
			telegramProvider, err := telegram.NewProvider(a.config.Telegram.BotToken, a.logger)
			if err != nil {
				a.logger.Error("failed to init telegram provider", zap.Error(err))
			} else {
				providers = append(providers, telegramProvider)
				a.logger.Info("Telegram Stars provider initialized")
			}
		}
	}

	// YooMoney провайдер (опционально, в коде есть internal/provider/yoomoney/yoomoney.go,
	// но регистрация выключена — кода для конфига пока нет).
	// TODO: добавить YooMoneyConfig + инициализацию по аналогии с WATA/Platega.
	// if a.config.YooMoney.Enabled {
	// 	yoomoneyProvider := yoomoney.NewProvider(
	// 		a.config.YooMoney.WalletID,
	// 		a.config.YooMoney.SecretKey,
	// 		a.config.YooMoney.ReturnURL,
	// 		a.config.YooMoney.NotifyURL,
	// 		a.logger,
	// 	)
	// 	providers = append(providers, yoomoneyProvider)
	// 	a.logger.Info("YooMoney provider initialized")
	// }

	// WATA H2H провайдер (опционально, управляется WATA_ENABLED).
	if a.config.Wata.Enabled {
		wataProvider := wata.NewProvider(wata.Config{
			BaseURL:     a.config.Wata.BaseURL,
			AccessToken: a.config.Wata.AccessToken,
			SuccessURL:  a.config.Wata.SuccessURL,
			FailURL:     a.config.Wata.FailURL,
			LinkTTL:     a.config.Wata.LinkTTL,
			Logger:      a.logger,
		})
		providers = append(providers, wataProvider)
		a.logger.Info("WATA provider initialized",
			zap.String("base_url", a.config.Wata.BaseURL),
			zap.Duration("link_ttl", a.config.Wata.LinkTTL),
		)
	}

	// Platega.io провайдер (опционально, управляется PLATEGA_ENABLED).
	// См. docs/services/platega-integration.md.
	if a.config.Platega.Enabled {
		plategaProvider := platega.NewProvider(platega.Config{
			BaseURL:       a.config.Platega.BaseURL,
			MerchantID:    a.config.Platega.MerchantID,
			APISecret:     a.config.Platega.APISecret,
			SuccessURL:    a.config.Platega.SuccessURL,
			FailURL:       a.config.Platega.FailURL,
			DefaultMethod: a.config.Platega.DefaultMethod,
			Logger:        a.logger,
		})
		providers = append(providers, plategaProvider)
		a.logger.Info("Platega provider initialized",
			zap.String("base_url", a.config.Platega.BaseURL),
			zap.Int("default_method", a.config.Platega.DefaultMethod),
		)
	}

	// ЮKassa провайдер (опционально)
	// TODO: реализовать YooKassaProvider
	// if a.config.YooKassa.ShopID != "" {
	// 	yookassaProvider := yookassa.NewProvider(...)
	// 	providers = append(providers, yookassaProvider)
	// 	a.logger.Info("YooKassa provider initialized")
	// }

	return providers
}

func (a *App) Start() error {
	addr := fmt.Sprintf("%s:%d", a.config.GRPC.Host, a.config.GRPC.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	a.logger.Info("Payment Service started", zap.String("addr", addr))

	go func() {
		if err := a.grpcServer.Serve(lis); err != nil {
			a.logger.Error("grpc serve error", zap.Error(err))
		}
	}()

	// Sentinel: фоновый цикл сканит таблицу payments на застрявшие в
	// промежуточных статусах и добивает state machine. Контекст отменяется
	// в Stop() — это сигнал воркеру выйти из цикла.
	if a.sentinel != nil {
		ctx, cancel := context.WithCancel(context.Background())
		a.sentinelCancel = cancel
		go a.sentinel.Start(ctx, &a.sentinelWg)
	}

	return nil
}

func (a *App) Stop(ctx context.Context) error {
	// Остановим sentinel первым: после shutdown'а gRPC у нас уже не будет
	// валидных gRPC connection'ов к соседям, и попытка ResumePaid упадёт.
	if a.sentinelCancel != nil {
		a.sentinelCancel()
		a.sentinelWg.Wait()
	}
	a.grpcServer.GracefulStop()
	return a.closer.CloseAll(ctx)
}
