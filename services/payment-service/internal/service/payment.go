package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/vpn/payment-service/internal/model"
	"github.com/vpn/payment-service/internal/provider"
	"github.com/vpn/payment-service/internal/repository"
	subpb "github.com/vpn/shared/pkg/proto/subscription/v1"
	vpnpb "github.com/vpn/shared/pkg/proto/vpn/v1"
	"go.uber.org/zap"
)

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
	log       *zap.Logger
}

// New создаёт новый PaymentService с набором провайдеров.
func New(
	repo *repository.PaymentRepository,
	providers []provider.PaymentProvider,
	sub subpb.SubscriptionServiceClient,
	vpn vpnpb.VPNServiceClient,
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
	payment := &model.Payment{
		UserID:      userID,
		PlanID:      planID,
		MaxDevices:  maxDevices,
		AmountStars: priceStars,
		AmountRUB:   priceRUB,
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
		Currency:    "XTR", // TODO: определять по провайдеру
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

// handleSuccessfulPayment обрабатывает успешный платёж.
func (s *PaymentService) handleSuccessfulPayment(ctx context.Context, event *provider.WebhookEvent) error {
	// 1. Находим платёж по external_id (идемпотентность)
	payment, err := s.repo.GetByExternalID(ctx, event.ExternalID)
	if err != nil {
		s.log.Error("payment not found",
			zap.String("external_id", event.ExternalID),
			zap.Error(err),
		)
		return fmt.Errorf("payment not found: %w", err)
	}

	// 2. Проверяем что платёж ещё не обработан (идемпотентность)
	if payment.Status == model.StatusPaid {
		s.log.Info("payment already processed (idempotent)",
			zap.Int64("payment_id", payment.ID),
			zap.String("external_id", event.ExternalID),
		)
		return nil
	}

	// 3. Обновляем статус платежа
	if err := s.repo.MarkPaid(ctx, payment.ID, event.Metadata); err != nil {
		return fmt.Errorf("mark paid: %w", err)
	}

	// 4. Создаём подписку
	subResp, err := s.sub.CreateSubscription(ctx, &subpb.CreateSubscriptionRequest{
		UserId:     payment.UserID,
		PlanId:     payment.PlanID,
		MaxDevices: payment.MaxDevices,
	})
	if err != nil {
		s.log.Error("create subscription failed",
			zap.Int64("payment_id", payment.ID),
			zap.Int64("user_id", payment.UserID),
			zap.Error(err),
		)
		return fmt.Errorf("create subscription: %w", err)
	}

	// 5. Создаём VPN пользователя
	vpnResp, err := s.vpn.CreateVPNUser(ctx, &vpnpb.CreateVPNUserRequest{
		UserId:         payment.UserID,
		SubscriptionId: subResp.GetSubscription().GetId(),
	})
	if err != nil {
		s.log.Error("create vpn user failed",
			zap.Int64("payment_id", payment.ID),
			zap.Int64("user_id", payment.UserID),
			zap.Int64("subscription_id", subResp.GetSubscription().GetId()),
			zap.Error(err),
		)
		return fmt.Errorf("create vpn user: %w", err)
	}

	s.log.Info("payment processed successfully",
		zap.Int64("payment_id", payment.ID),
		zap.Int64("user_id", payment.UserID),
		zap.Int64("subscription_id", subResp.GetSubscription().GetId()),
		zap.String("vpn_uuid", vpnResp.GetVpnUser().GetUuid()),
	)

	return nil
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
