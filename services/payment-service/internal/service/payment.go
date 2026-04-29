package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/vpn/payment-service/internal/model"
	"github.com/vpn/payment-service/internal/notifier"
	"github.com/vpn/payment-service/internal/provider"
	"github.com/vpn/payment-service/internal/repository"
	authpb "github.com/vpn/shared/pkg/proto/auth/v1"
	referralpb "github.com/vpn/shared/pkg/proto/referral/v1"
	subpb "github.com/vpn/shared/pkg/proto/subscription/v1"
	vpnpb "github.com/vpn/shared/pkg/proto/vpn/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// ReferralClient — узкий интерфейс к referral-service. Может быть nil
// (referral не подключён) — тогда хук молча пропускается.
type ReferralClient interface {
	ApplyBonus(ctx context.Context, req *referralpb.ApplyBonusRequest, opts ...grpc.CallOption) (*referralpb.ApplyBonusResponse, error)
}

// ErrInvalidPlan — не нашли такой (plan_id, max_devices) в device_addon_pricing.
var ErrInvalidPlan = errors.New("invalid plan or max_devices")

// ErrUnknownProvider — неизвестный провайдер.
var ErrUnknownProvider = errors.New("unknown payment provider")

// PaymentService — оркестратор платежного флоу с поддержкой нескольких провайдеров.
//
// Следуя SOLID принципам:
// - Single Responsibility: сервис координирует работу провайдеров, но не реализует их логику
// - Open/Closed: легко добавить новый провайдер без изменения сервиса
// - Liskov Substitution: любой провайдер взаимозаменяем
// - Interface Segregation: провайдеры реализуют только нужные методы
// - Dependency Inversion: сервис зависит от интерфейса, а не от конкретных провайдеров
type PaymentService struct {
	repo      *repository.PaymentRepository
	providers map[string]provider.PaymentProvider // ключ = имя провайдера
	sub       subpb.SubscriptionServiceClient
	vpn       vpnpb.VPNServiceClient
	auth      authpb.AuthServiceClient // только для user_id → telegram_id перед NotifyPaid
	referral  ReferralClient           // nil если referral-service не подключён
	notifier  *notifier.Telegram       // nil если бот не настроен — тогда уведомления не шлём
	log       *zap.Logger
}

// New создаёт новый PaymentService с набором провайдеров.
//
// notifier — опциональный nil; если задан, после успешной активации
// подписки юзеру улетает push-сообщение в чат с ботом.
func New(
	repo *repository.PaymentRepository,
	providers []provider.PaymentProvider,
	sub subpb.SubscriptionServiceClient,
	vpn vpnpb.VPNServiceClient,
	auth authpb.AuthServiceClient,
	referral ReferralClient,
	tgNotifier *notifier.Telegram,
	log *zap.Logger,
) *PaymentService {
	// Создаём map провайдеров для быстрого доступа по имени
	providerMap := make(map[string]provider.PaymentProvider)
	for _, p := range providers {
		providerMap[p.Name()] = p
		log.Info("payment provider registered", zap.String("provider", p.Name()))
	}

	return &PaymentService{
		repo:      repo,
		providers: providerMap,
		sub:       sub,
		vpn:       vpn,
		auth:      auth,
		referral:  referral,
		notifier:  tgNotifier,
		log:       log,
	}
}

// CreateInvoice создаёт платёж через указанный провайдер.
func (s *PaymentService) CreateInvoice(
	ctx context.Context,
	userID int64,
	planID int32,
	maxDevices int32,
	providerName string,
) (*model.Payment, string, error) {
	// 1. Проверяем что провайдер существует
	prov, ok := s.providers[providerName]
	if !ok {
		return nil, "", fmt.Errorf("%w: %s", ErrUnknownProvider, providerName)
	}

	// 2. Получаем цену из subscription-service
	pricing, err := s.sub.GetDevicePricing(ctx, &subpb.GetDevicePricingRequest{PlanId: planID})
	if err != nil {
		return nil, "", fmt.Errorf("get pricing: %w", err)
	}

	var priceStars int32
	var priceRUB float64
	var planName string
	var found bool

	for _, p := range pricing.GetPrices() {
		if p.GetMaxDevices() == maxDevices {
			priceStars = p.GetPriceStars() // уже вычислено на стороне subscription-service из currency_rates
			if _, err := fmt.Sscanf(p.GetPrice(), "%f", &priceRUB); err != nil {
				return nil, "", fmt.Errorf("parse price_rub %q: %w", p.GetPrice(), err)
			}
			planName = p.GetPlanName()
			found = true
			break
		}
	}

	if !found || priceRUB <= 0 {
		return nil, "", fmt.Errorf("%w: plan=%d devices=%d (price_rub=%.2f)",
			ErrInvalidPlan, planID, maxDevices, priceRUB)
	}

	// 3. Создаём pending-запись в БД
	currency := "RUB"
	if providerName == model.ProviderTelegramStars {
		currency = "XTR"
	}
	payment := &model.Payment{
		UserID:      userID,
		PlanID:      planID,
		MaxDevices:  maxDevices,
		AmountStars: priceStars,
		AmountRUB:   priceRUB,
		Currency:    currency,
		Provider:    providerName,
		Status:      model.StatusPending,
	}

	if _, err := s.repo.CreatePending(ctx, payment); err != nil {
		return nil, "", fmt.Errorf("create pending payment: %w", err)
	}

	// 4. Создаём invoice через провайдера
	description := fmt.Sprintf("VPN %s × %d устройств", planName, maxDevices)
	
	invoice, err := prov.CreateInvoice(ctx, &provider.CreateInvoiceRequest{
		UserID:      userID,
		PlanID:      planID,
		MaxDevices:  maxDevices,
		AmountStars: priceStars,
		AmountRUB:   priceRUB,
		Currency:    currency,
		Description: description,
		Metadata: map[string]string{
			"payment_id": fmt.Sprintf("%d", payment.ID),
		},
	})
	if err != nil {
		s.log.Error("provider create invoice failed",
			zap.Int64("payment_id", payment.ID),
			zap.String("provider", providerName),
			zap.Error(err),
		)
		return nil, "", fmt.Errorf("provider create invoice: %w", err)
	}

	// 5. Обновляем external_id если провайдер его вернул
	if invoice.ExternalID != "" {
		payment.ExternalID = invoice.ExternalID
		if err := s.repo.UpdateExternalID(ctx, payment.ID, invoice.ExternalID); err != nil {
			s.log.Warn("failed to update external_id",
				zap.Int64("payment_id", payment.ID),
				zap.String("external_id", invoice.ExternalID),
				zap.Error(err),
			)
		}
	}

	s.log.Info("invoice created",
		zap.Int64("payment_id", payment.ID),
		zap.Int64("user_id", userID),
		zap.Int32("plan_id", planID),
		zap.Int32("max_devices", maxDevices),
		zap.String("provider", providerName),
		zap.String("invoice_link", invoice.InvoiceLink),
	)

	return payment, invoice.InvoiceLink, nil
}

// HandleWebhook обрабатывает webhook от провайдера.
func (s *PaymentService) HandleWebhook(
	ctx context.Context,
	providerName string,
	payload []byte,
	signature string,
) error {
	// 1. Получаем провайдера
	prov, ok := s.providers[providerName]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownProvider, providerName)
	}

	// 2. Валидируем webhook
	if err := prov.ValidateWebhook(payload, signature); err != nil {
		s.log.Warn("webhook validation failed",
			zap.String("provider", providerName),
			zap.Error(err),
		)
		return fmt.Errorf("webhook validation failed: %w", err)
	}

	// 3. Обрабатываем webhook
	event, err := prov.HandleWebhook(ctx, payload, signature)
	if err != nil {
		s.log.Error("webhook handling failed",
			zap.String("provider", providerName),
			zap.Error(err),
		)
		return fmt.Errorf("webhook handling failed: %w", err)
	}

	// 4. Обрабатываем событие в зависимости от статуса
	switch event.Status {
	case "paid":
		return s.handleSuccessfulPayment(ctx, event)
	case "cancelled":
		return s.handleCancelledPayment(ctx, event)
	case "failed":
		return s.handleFailedPayment(ctx, event)
	default:
		s.log.Warn("unknown payment status",
			zap.String("status", event.Status),
			zap.String("external_id", event.ExternalID),
		)
		return nil
	}
}

// handleSuccessfulPayment обрабатывает успешный платёж как state machine.
//
// Каждый из 3 шагов — атомарный чек-поинт в БД (миграция 003):
//
//	pending  ──[step1 MarkPaidDBOnly]──▶  paid_db_only
//	         ──[step2 CreateSubscription + MarkSubscriptionDone]──▶  paid_subscription_done
//	         ──[step3 CreateVPNUser + MarkComplete]──▶  paid  (финал)
//
// Если webhook ретрается после сбоя — мы видим текущий статус и продолжаем
// с того шага, где остановились. Финальный paid → early return + повторно
// notify не шлём (защита от дублирующих сообщений).
//
// Telegram notify шлётся ТОЛЬКО когда status стал paid — гарантирует что
// мы не уведомим юзера до полной активации подписки.
func (s *PaymentService) handleSuccessfulPayment(ctx context.Context, event *provider.WebhookEvent) error {
	// Шаг 0: Находим платёж.
	payment, err := s.repo.GetByExternalID(ctx, event.ExternalID)
	if err != nil {
		s.log.Error("payment not found",
			zap.String("external_id", event.ExternalID),
			zap.Error(err),
		)
		return fmt.Errorf("payment not found: %w", err)
	}

	// Финальный успех — early return, ничего не делаем (включая повторный notify).
	if payment.Status == model.StatusPaid {
		s.log.Info("payment already processed (idempotent)",
			zap.Int64("payment_id", payment.ID),
			zap.String("external_id", event.ExternalID),
		)
		return nil
	}

	// Платёж в "уж точно не paid" состоянии (cancelled/failed/refunded) —
	// это аномалия (webhook прислал paid, но мы уже отменили). Логируем и
	// не двигаем стейт; админ разберётся.
	if payment.Status != model.StatusPending && !model.IsPaidIntermediate(payment.Status) {
		s.log.Warn("payment in non-paid state — refusing to process paid webhook",
			zap.Int64("payment_id", payment.ID),
			zap.String("status", payment.Status),
		)
		return nil
	}

	// ── Шаг 1: pending → paid_db_only ─────────────────────────────────
	if payment.Status == model.StatusPending {
		if err := s.repo.MarkPaidDBOnly(ctx, payment.ID, event.Metadata); err != nil {
			return fmt.Errorf("mark paid_db_only: %w", err)
		}
		payment.Status = model.StatusPaidDBOnly
		s.log.Info("payment step1: marked paid_db_only", zap.Int64("payment_id", payment.ID))
	}

	// ── Шаг 2: paid_db_only → paid_subscription_done ──────────────────
	// CreateSubscription у subscription-service идемпотентен (upsert по user_id),
	// поэтому даже если на ретрае мы проскочим этот блок и потом упадём перед
	// MarkSubscriptionDone — следующий ретрай вызовет subscription повторно
	// без вреда (тот же sub_id).
	var subscriptionID int64
	var subPlanName, subExpiresAt string
	if payment.Status == model.StatusPaidDBOnly {
		subResp, err := s.sub.CreateSubscription(ctx, &subpb.CreateSubscriptionRequest{
			UserId:     payment.UserID,
			PlanId:     payment.PlanID,
			MaxDevices: payment.MaxDevices,
		})
		if err != nil {
			s.log.Error("step2: create subscription failed",
				zap.Int64("payment_id", payment.ID),
				zap.Int64("user_id", payment.UserID),
				zap.Error(err),
			)
			return fmt.Errorf("create subscription: %w", err)
		}
		sub := subResp.GetSubscription()
		subscriptionID = sub.GetId()
		subPlanName = sub.GetPlanName()
		subExpiresAt = sub.GetExpiresAt()

		if err := s.repo.MarkSubscriptionDone(ctx, payment.ID, subscriptionID); err != nil {
			return fmt.Errorf("mark paid_subscription_done: %w", err)
		}
		payment.Status = model.StatusPaidSubscription
		s.log.Info("payment step2: subscription created",
			zap.Int64("payment_id", payment.ID),
			zap.Int64("subscription_id", subscriptionID),
		)
	} else {
		// Уже paid_subscription_done → подписка создана в прошлый раз.
		// Достаём её id из metadata, остальные поля (plan_name/expires_at) нужны
		// только для notify — берём через GetActiveSubscription.
		subscriptionID, err = parseSubscriptionIDFromMetadata(payment.Metadata)
		if err != nil {
			s.log.Error("step2: cannot recover subscription_id from metadata",
				zap.Int64("payment_id", payment.ID),
				zap.Error(err),
			)
			return fmt.Errorf("recover subscription_id: %w", err)
		}
		activeResp, err := s.sub.GetActiveSubscription(ctx, &subpb.GetActiveSubscriptionRequest{
			UserId: payment.UserID,
		})
		if err == nil {
			sub := activeResp.GetSubscription()
			subPlanName = sub.GetPlanName()
			subExpiresAt = sub.GetExpiresAt()
		}
		s.log.Info("payment step2: skipping (already done), recovered from metadata",
			zap.Int64("payment_id", payment.ID),
			zap.Int64("subscription_id", subscriptionID),
		)
	}

	// ── Шаг 3: paid_subscription_done → paid ──────────────────────────
	// CreateVPNUser идемпотентен (ON CONFLICT DO NOTHING) — повторный вызов
	// безопасен.
	vpnResp, err := s.vpn.CreateVPNUser(ctx, &vpnpb.CreateVPNUserRequest{
		UserId:         payment.UserID,
		SubscriptionId: subscriptionID,
	})
	if err != nil {
		s.log.Error("step3: create vpn user failed",
			zap.Int64("payment_id", payment.ID),
			zap.Int64("user_id", payment.UserID),
			zap.Int64("subscription_id", subscriptionID),
			zap.Error(err),
		)
		return fmt.Errorf("create vpn user: %w", err)
	}

	if err := s.repo.MarkComplete(ctx, payment.ID); err != nil {
		// Неудача на финальном UPDATE — некритично: VPN-юзер уже создан, на
		// следующем ретрае ON CONFLICT отработает + UPDATE доедет.
		s.log.Error("step3: mark complete failed (will retry on next webhook)",
			zap.Int64("payment_id", payment.ID),
			zap.Error(err),
		)
		return fmt.Errorf("mark complete: %w", err)
	}

	s.log.Info("payment processed successfully (status=paid)",
		zap.Int64("payment_id", payment.ID),
		zap.Int64("user_id", payment.UserID),
		zap.Int64("subscription_id", subscriptionID),
		zap.String("vpn_uuid", vpnResp.GetVpnUser().GetUuid()),
	)

	// Реферальный хук — best-effort. Если у юзера есть inviter, начислит
	// партнёрский % на баланс. Идемпотентно по payment_id (UNIQUE на
	// referral_bonuses.payment_id). Запускаем в горутине, чтобы не
	// тормозить webhook-handler.
	if s.referral != nil && payment.AmountRUB > 0 {
		go func(invitedID, paymentID int64, amountRUB float64) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := s.referral.ApplyBonus(ctx, &referralpb.ApplyBonusRequest{
				InvitedUserId:    invitedID,
				PaymentAmountRub: fmt.Sprintf("%.2f", amountRUB),
				PaymentId:        paymentID,
			})
			if err != nil {
				s.log.Warn("referral.ApplyBonus failed (non-blocking)",
					zap.Int64("payment_id", paymentID),
					zap.Int64("invited_id", invitedID),
					zap.Error(err),
				)
				return
			}
			if resp.Applied {
				s.log.Info("referral bonus applied",
					zap.Int64("payment_id", paymentID),
					zap.Int64("inviter_id", resp.InviterUserId),
					zap.String("balance_amount", resp.BalanceAmountRub),
				)
			} else if resp.AlreadyApplied {
				s.log.Info("referral bonus already applied (idempotent)",
					zap.Int64("payment_id", paymentID),
				)
			}
		}(payment.UserID, payment.ID, payment.AmountRUB)
	}

	// Push-уведомление в чат с ботом — только при достижении финального paid.
	// Telegram chat_id == users.telegram_id, а в payment у нас внутренний
	// users.id — резолвим через auth-service. Запускаем в горутине с
	// собственным контекстом, чтобы webhook handler не блокировался на
	// сети к Telegram API.
	if s.notifier != nil && subPlanName != "" {
		userResp, err := s.auth.GetUser(ctx, &authpb.GetUserRequest{UserId: payment.UserID})
		if err != nil || userResp.GetUser() == nil || userResp.GetUser().GetTelegramId() == 0 {
			s.log.Warn("notify skipped: cannot resolve telegram_id",
				zap.Int64("payment_id", payment.ID),
				zap.Int64("user_id", payment.UserID),
				zap.Error(err),
			)
		} else {
			go func(chatID int64, planName, expiresAt string, amountRUB float64) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				s.notifier.NotifyPaid(ctx, chatID, planName, expiresAt, amountRUB)
			}(userResp.GetUser().GetTelegramId(), subPlanName, subExpiresAt, payment.AmountRUB)
		}
	}

	return nil
}

// ResumePaid — публичная точка входа для sentinel-cron'а. Вызывает state
// machine `handleSuccessfulPayment` для зависшего платежа без webhook'а
// от провайдера. Идемпотентность — за счёт самой state machine: если
// шаг уже сделан, он будет пропущен.
//
// event тут содержит минимум — только ExternalID для GetByExternalID
// (всё остальное берётся из payment в БД).
func (s *PaymentService) ResumePaid(ctx context.Context, payment *model.Payment) error {
	event := &provider.WebhookEvent{
		ExternalID: payment.ExternalID,
		Status:     "paid",
	}
	return s.handleSuccessfulPayment(ctx, event)
}

// parseSubscriptionIDFromMetadata вытаскивает subscription_id из
// payment.metadata, куда он попал на шаге 2 (см. repo.MarkSubscriptionDone).
// Используется при ретрае webhook'а, когда мы возобновляем flow с шага 3.
func parseSubscriptionIDFromMetadata(metadata map[string]string) (int64, error) {
	v, ok := metadata["subscription_id"]
	if !ok {
		return 0, errors.New("metadata.subscription_id not set (paid_subscription_done without sub_id?)")
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid metadata.subscription_id %q: %w", v, err)
	}
	return id, nil
}

// handleCancelledPayment обрабатывает отменённый платёж.
func (s *PaymentService) handleCancelledPayment(ctx context.Context, event *provider.WebhookEvent) error {
	payment, err := s.repo.GetByExternalID(ctx, event.ExternalID)
	if err != nil {
		return fmt.Errorf("payment not found: %w", err)
	}

	if err := s.repo.MarkCancelled(ctx, payment.ID); err != nil {
		return fmt.Errorf("mark cancelled: %w", err)
	}

	s.log.Info("payment cancelled",
		zap.Int64("payment_id", payment.ID),
		zap.String("external_id", event.ExternalID),
	)

	return nil
}

// handleFailedPayment обрабатывает неудачный платёж.
func (s *PaymentService) handleFailedPayment(ctx context.Context, event *provider.WebhookEvent) error {
	payment, err := s.repo.GetByExternalID(ctx, event.ExternalID)
	if err != nil {
		return fmt.Errorf("payment not found: %w", err)
	}

	if err := s.repo.MarkFailed(ctx, payment.ID); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}

	s.log.Info("payment failed",
		zap.Int64("payment_id", payment.ID),
		zap.String("external_id", event.ExternalID),
	)

	return nil
}

// ListPayments возвращает историю платежей пользователя.
func (s *PaymentService) ListPayments(ctx context.Context, userID int64, limit, offset int32) ([]*model.Payment, error) {
	return s.repo.ListByUser(ctx, userID, limit, offset)
}

// GetPayment возвращает платёж по ID.
func (s *PaymentService) GetPayment(ctx context.Context, paymentID int64) (*model.Payment, error) {
	return s.repo.GetByID(ctx, paymentID)
}
