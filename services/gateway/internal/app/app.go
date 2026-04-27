package app

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/vpn/gateway/internal/client"
	"github.com/vpn/gateway/internal/config"
	"github.com/vpn/gateway/internal/handler"
	gwmw "github.com/vpn/gateway/internal/middleware"
	"github.com/vpn/platform/pkg/closer"
	authmw "github.com/vpn/platform/pkg/middleware"
	"github.com/vpn/platform/pkg/telegram"
	"go.uber.org/zap"
)

type App struct {
	config             *config.Config
	logger             *zap.Logger
	httpServer         *http.Server
	authClient         *client.AuthClient
	subscriptionClient *client.SubscriptionClient
	vpnClient          *client.VPNClient
	paymentClient      *client.PaymentClient
	closer             *closer.Closer
}

func New(logger *zap.Logger) (*App, error) {
	cfg, err := config.New()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	app := &App{
		config: cfg,
		logger: logger,
		closer: closer.New(),
	}

	// Initialize gRPC clients
	if err := app.initClients(); err != nil {
		return nil, fmt.Errorf("failed to init clients: %w", err)
	}

	return app, nil
}

func (a *App) initClients() error {
	// Auth client
	authClient, err := client.NewAuthClient(a.config.Services.AuthAddr, a.logger)
	if err != nil {
		return fmt.Errorf("failed to create auth client: %w", err)
	}
	a.authClient = authClient
	a.closer.Add(func(ctx context.Context) error {
		return a.authClient.Close()
	})

	// Subscription client
	subscriptionClient, err := client.NewSubscriptionClient(a.config.Services.SubscriptionAddr, a.logger)
	if err != nil {
		return fmt.Errorf("failed to create subscription client: %w", err)
	}
	a.subscriptionClient = subscriptionClient
	a.closer.Add(func(ctx context.Context) error {
		return a.subscriptionClient.Close()
	})

	// VPN client
	vpnClient, err := client.NewVPNClient(a.config.Services.VPNAddr, a.logger)
	if err != nil {
		return fmt.Errorf("failed to create vpn client: %w", err)
	}
	a.vpnClient = vpnClient
	a.closer.Add(func(ctx context.Context) error {
		return a.vpnClient.Close()
	})

	// Payment client
	paymentClient, err := client.NewPaymentClient(a.config.Services.PaymentAddr, a.logger)
	if err != nil {
		return fmt.Errorf("failed to create payment client: %w", err)
	}
	a.paymentClient = paymentClient
	a.closer.Add(func(ctx context.Context) error {
		return a.paymentClient.Close()
	})

	a.logger.Info("gRPC clients initialized",
		zap.String("auth", a.config.Services.AuthAddr),
		zap.String("subscription", a.config.Services.SubscriptionAddr),
		zap.String("vpn", a.config.Services.VPNAddr),
		zap.String("payment", a.config.Services.PaymentAddr),
	)

	return nil
}

func (a *App) Start() error {
	// Setup router
	router := chi.NewRouter()

	// Middleware
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(60 * time.Second))

	// CORS
	router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Health check
	router.Get("/health", handler.HealthCheck)

	// Handlers
	authHandler := handler.NewAuthHandler(a.authClient, a.subscriptionClient, a.vpnClient, a.logger)
	subscriptionHandler := handler.NewSubscriptionHandler(a.subscriptionClient, a.logger)
	vpnHandler := handler.NewVPNHandler(a.vpnClient, a.logger)
	paymentHandler := handler.NewPaymentHandler(a.paymentClient, a.config.Telegram.WebhookSecret, a.logger)
	subscriptionConfigHandler := handler.NewSubscriptionConfigHandler(a.vpnClient, a.logger)
	bonusHandler := handler.NewBonusHandler(a.subscriptionClient, a.logger, a.config.Telegram.BotToken, a.config.Telegram.ChannelUsername)

	// Telegram Bot Handler для команд и callback'ов
	telegramClient := telegram.New(a.config.Telegram.BotToken)
	telegramBotHandler := handler.NewTelegramBotHandler(telegramClient, a.subscriptionClient, a.authClient, a.logger, a.config.Telegram.ChannelUsername)

	// JWT middleware для защищённых ручек. Секрет — общий с Auth Service.
	jwtMiddleware := authmw.JWTMiddleware(a.config.JWT.Secret)

	// Rate-limit для публичного subscription endpoint (нет JWT → легко
	// перебирать токены). 30 req/min на IP — достаточно легитимных клиентов
	// (HAPP, Hiddify обновляют Profile-Update-Interval=1h, Mini App
	// дёргает при нажатии кнопки), брутфорс отрезает.
	subscriptionLimiter := gwmw.NewRateLimiter(30, time.Minute)
	a.closer.Add(func(ctx context.Context) error {
		subscriptionLimiter.Stop()
		return nil
	})

	// API routes
	router.Route("/api/v1", func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok","service":"vpn-gateway"}`))
		})

		// ───── Публичные ручки (без JWT) ─────────────────────────────
		// Логин через Telegram initData — откуда ещё брать токен.
		r.Post("/auth/validate", authHandler.ValidateTelegramUser)
		// Прайс-лист доступен и до логина (приветственный экран Mini App).
		r.Get("/subscriptions/plans", subscriptionHandler.ListPlans)
		r.Get("/subscriptions/plans/{planId}/pricing", subscriptionHandler.GetDevicePricing)
		// Subscription config для VPN клиентов (Happ, V2RayNG, etc.).
		// Ratelimit — защита от брутфорса токенов (см. subscriptionLimiter выше).
		r.With(subscriptionLimiter.Handler).Get("/subscription/{token}", subscriptionConfigHandler.SubscriptionConfig)
		// Telegram webhook — публичный, но защищён shared-секретом
		// в заголовке X-Telegram-Bot-Api-Secret-Token (проверяется в handler'е).
		r.Post("/telegram/webhook", paymentHandler.TelegramWebhook)
		// Telegram Bot webhook для команд и callback'ов (например /bonus)
		r.Post("/telegram/bot-webhook", telegramBotHandler.HandleBotWebhook)

		// Универсальный webhook handler для всех провайдеров
		// /api/v1/payments/webhook/telegram_stars
		// /api/v1/payments/webhook/yoomoney
		// /api/v1/payments/webhook/yookassa
		// /api/v1/payments/webhook/wata
		r.Post("/payments/webhook/{provider}", paymentHandler.HandleWebhook)

		// ───── Защищённые ручки (Authorization: Bearer <JWT>) ─────────
		r.Group(func(r chi.Router) {
			r.Use(jwtMiddleware)

			// Auth
			r.Get("/auth/users/{userId}", authHandler.GetUser)

			// Subscriptions
			r.Get("/subscriptions/active", subscriptionHandler.GetActiveSubscription)
			r.Post("/subscriptions", subscriptionHandler.CreateSubscription)
			r.Get("/subscriptions/history", subscriptionHandler.GetSubscriptionHistory)

			// VPN
			r.Get("/vpn/servers", vpnHandler.ListServers)
			r.Get("/vpn/servers/{serverId}/link", vpnHandler.GetVLESSLink)
			r.Get("/vpn/connections", vpnHandler.GetActiveConnections)
			r.Delete("/vpn/devices/{connectionId}", vpnHandler.DisconnectDevice)
			// Subscription token для Mini App: фронт вызывает на /connect,
			// собирает URL подписки и деплинки для клиентов (Happ/Hiddify/…).
			r.Get("/vpn/subscription-token", vpnHandler.GetSubscriptionToken)

			// Payments
			r.Post("/payments", paymentHandler.CreateInvoice)
			r.Get("/payments", paymentHandler.ListPayments)

			// Channel Bonus
			r.Post("/bonus/check-subscription", bonusHandler.CheckChannelSubscription)
			r.Post("/bonus/claim", bonusHandler.ClaimChannelBonus)
		})
	})

	// HTTP Server
	addr := fmt.Sprintf("%s:%d", a.config.HTTP.Host, a.config.HTTP.Port)
	a.httpServer = &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Register closer
	a.closer.Add(func(ctx context.Context) error {
		return a.httpServer.Shutdown(ctx)
	})

	// Start server
	a.logger.Info("Starting HTTP server", zap.String("addr", addr))
	go func() {
		if err := a.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.logger.Fatal("HTTP server error", zap.Error(err))
		}
	}()

	return nil
}

func (a *App) Stop(ctx context.Context) error {
	return a.closer.CloseAll(ctx)
}
