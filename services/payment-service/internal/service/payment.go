package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/vpn/payment-service/internal/model"
	"github.com/vpn/payment-service/internal/repository"
	"github.com/vpn/platform/pkg/telegram"
	subpb "github.com/vpn/shared/pkg/proto/subscription/v1"
	vpnpb "github.com/vpn/shared/pkg/proto/vpn/v1"
	"go.uber.org/zap"
)

// ErrInvalidPlan — не нашли такой (plan_id, max_devices) в device_addon_pricing.
var ErrInvalidPlan = errors.New("invalid plan or max_devices")

// PaymentService — оркестратор платежного флоу.
//
//   - CreateInvoice: price_stars из subscription-service → INSERT pending → t.me/ссылка
//   - HandleUpdate: парсит Telegram update, роутит на нужный handler
//   - handleSuccessfulPayment: идемпотентный mark-paid + Create{Subscription,VPNUser}
//   - handleRefund: mark-refunded + Cancel{Subscription} + DisableVPNUser
type PaymentService struct {
	repo *repository.PaymentRepository
	tg   *telegram.Client
	sub  subpb.SubscriptionServiceClient
	vpn  vpnpb.VPNServiceClient
	log  *zap.Logger
}

func New(
	repo *repository.PaymentRepository,
	tg *telegram.Client,
	sub subpb.SubscriptionServiceClient,
	vpn vpnpb.VPNServiceClient,
	log *zap.Logger,
) *PaymentService {
	return &PaymentService{repo: repo, tg: tg, sub: sub, vpn: vpn, log: log}
}

// CreateInvoice — шаг 1: создаём pending-запись и возвращаем t.me/ссылку.
func (s *PaymentService) CreateInvoice(ctx context.Context, userID int64, planID, maxDevices int32) (*model.Payment, string, error) {
	// 1. Узнать цену в Stars у subscription-service.
	pricing, err := s.sub.GetDevicePricing(ctx, &subpb.GetDevicePricingRequest{PlanId: planID})
	if err != nil {
		return nil, "", fmt.Errorf("get pricing: %w", err)
	}
	var priceStars int32
	var planName string
	for _, p := range pricing.GetPrices() {
		if p.GetMaxDevices() == maxDevices {
			priceStars = p.GetPriceStars()
			planName = p.GetPlanName()
			break
		}
	}
	if priceStars <= 0 {
		return nil, "", fmt.Errorf("%w: plan=%d devices=%d (price_stars=%d)",
			ErrInvalidPlan, planID, maxDevices, priceStars)
	}

	// 2. Создать pending-запись.
	p := &model.Payment{
		UserID:      userID,
		PlanID:      planID,
		MaxDevices:  maxDevices,
		AmountStars: priceStars,
		Provider:    model.ProviderTelegramStars,
	}
	if _, err := s.repo.CreatePending(ctx, p); err != nil {
		return nil, "", err
	}

	// 3. Получить invoice_link у Telegram.
	// Payload — идентификатор платежа на нашей стороне: payment_id.
	// Telegram вернёт это в successful_payment.invoice_payload → мы найдём запись.
	title := fmt.Sprintf("VPN %s × %d устройств", planName, maxDevices)
	desc := fmt.Sprintf("Подписка на %s устройств(а). После оплаты ссылка появится в приложении.", strconv.Itoa(int(maxDevices)))

	link, err := s.tg.CreateInvoiceLink(ctx, telegram.CreateInvoiceLinkParams{
		Title:       title,
		Description: desc,
		Payload:     strconv.FormatInt(p.ID, 10),
		Currency:    "XTR",
		Prices: []telegram.LabeledPrice{
			{Label: planName, Amount: priceStars},
		},
	})
	if err != nil {
		// Payment в БД уже есть как pending; его можно retry-нуть или auto-fail cron-ом.
		s.log.Error("createInvoiceLink failed",
			zap.Int64("payment_id", p.ID), zap.Error(err))
		return nil, "", fmt.Errorf("telegram createInvoiceLink: %w", err)
	}

	s.log.Info("invoice created",
		zap.Int64("payment_id", p.ID),
		zap.Int64("user_id", userID),
		zap.Int32("amount_stars", priceStars),
	)
	return p, link, nil
}

// HandleUpdate — главная точка входа для webhook.
// Распознаёт три типа update:
//   - pre_checkout_query → answerPreCheckoutQuery(ok=true)
//   - message.successful_payment → markPaid + CreateSubscription + CreateVPNUser
//   - message.refunded_payment → markRefunded + CancelSubscription + DisableVPNUser
//
// Для всего остального возвращает handled=false / action="ignored".
func (s *PaymentService) HandleUpdate(ctx context.Context, raw []byte) (action string, err error) {
	var upd tgUpdate
	if err := json.Unmarshal(raw, &upd); err != nil {
		return "ignored", fmt.Errorf("parse update: %w", err)
	}

	if upd.PreCheckoutQuery != nil {
		return s.handlePreCheckout(ctx, upd.PreCheckoutQuery)
	}
	if upd.Message != nil && upd.Message.SuccessfulPayment != nil {
		return s.handleSuccessfulPayment(ctx, upd.Message.From.ID, upd.Message.SuccessfulPayment)
	}
	if upd.Message != nil && upd.Message.RefundedPayment != nil {
		return s.handleRefund(ctx, upd.Message.RefundedPayment)
	}
	return "ignored", nil
}

// handlePreCheckout подтверждает что мы готовы принять оплату.
// Если в БД нет pending с таким payment_id — отклоняем.
func (s *PaymentService) handlePreCheckout(ctx context.Context, pcq *tgPreCheckoutQuery) (string, error) {
	paymentID, _ := strconv.ParseInt(pcq.InvoicePayload, 10, 64)

	p, err := s.repo.GetByID(ctx, paymentID)
	if err != nil {
		// Отклоняем — оплата невалидна.
		_ = s.tg.AnswerPreCheckoutQuery(ctx, telegram.AnswerPreCheckoutQueryParams{
			PreCheckoutQueryID: pcq.ID,
			Ok:                 false,
			ErrorMessage:       "Заказ не найден. Попробуйте создать новый.",
		})
		return "pre_checkout_rejected", fmt.Errorf("payment not found: %d", paymentID)
	}
	if p.Status != model.StatusPending {
		_ = s.tg.AnswerPreCheckoutQuery(ctx, telegram.AnswerPreCheckoutQueryParams{
			PreCheckoutQueryID: pcq.ID,
			Ok:                 false,
			ErrorMessage:       "Этот заказ уже обработан.",
		})
		return "pre_checkout_rejected", fmt.Errorf("payment not pending: %d status=%s", paymentID, p.Status)
	}

	if err := s.tg.AnswerPreCheckoutQuery(ctx, telegram.AnswerPreCheckoutQueryParams{
		PreCheckoutQueryID: pcq.ID,
		Ok:                 true,
	}); err != nil {
		return "pre_checkout_rejected", fmt.Errorf("answerPreCheckoutQuery: %w", err)
	}
	s.log.Info("pre_checkout ok", zap.Int64("payment_id", paymentID))
	return "pre_checkout_ok", nil
}

// handleSuccessfulPayment — главный критичный путь. Должен быть ИДЕМПОТЕНТНЫМ.
func (s *PaymentService) handleSuccessfulPayment(ctx context.Context, tgUserID int64, sp *tgSuccessfulPayment) (string, error) {
	paymentID, err := strconv.ParseInt(sp.InvoicePayload, 10, 64)
	if err != nil {
		return "ignored", fmt.Errorf("invalid invoice_payload: %q", sp.InvoicePayload)
	}

	// Идемпотентность: если уже обработали этот telegram_payment_charge_id — выходим.
	if existing, _ := s.repo.GetByExternalID(ctx, sp.TelegramPaymentChargeID); existing != nil {
		s.log.Info("duplicate successful_payment, skipping",
			zap.String("charge_id", sp.TelegramPaymentChargeID),
			zap.Int64("payment_id", existing.ID),
		)
		return "paid_duplicate", nil
	}

	p, err := s.repo.GetByID(ctx, paymentID)
	if err != nil {
		return "ignored", fmt.Errorf("payment not found: %d", paymentID)
	}

	// 1. Пометить payment как paid.
	alreadyPaid, err := s.repo.MarkPaid(ctx, paymentID, sp.TelegramPaymentChargeID)
	if err != nil {
		return "paid_failed", err
	}
	if alreadyPaid {
		return "paid_duplicate", nil
	}

	// 2. Создать подписку.
	subResp, err := s.sub.CreateSubscription(ctx, &subpb.CreateSubscriptionRequest{
		UserId:     p.UserID,
		PlanId:     p.PlanID,
		MaxDevices: p.MaxDevices,
		TotalPrice: strconv.Itoa(int(p.AmountStars)), // TotalPrice хранится как строка (decimal), для Stars — просто число
	})
	if err != nil {
		// Платёж прошёл, но подписка не создалась — алерт! Будет alertmanager / UptimeRobot → срочный fix.
		s.log.Error("CreateSubscription failed after successful_payment",
			zap.Int64("payment_id", paymentID), zap.Error(err))
		return "paid_but_subscription_failed", err
	}
	subscriptionID := subResp.GetSubscription().GetId()

	// 3. Создать VPN user (регистрация в Xray).
	if _, err := s.vpn.CreateVPNUser(ctx, &vpnpb.CreateVPNUserRequest{
		UserId:         p.UserID,
		SubscriptionId: subscriptionID,
	}); err != nil {
		s.log.Error("CreateVPNUser failed after successful_payment",
			zap.Int64("payment_id", paymentID),
			zap.Int64("subscription_id", subscriptionID),
			zap.Error(err))
		return "paid_but_vpn_failed", err
	}

	s.log.Info("payment successfully processed",
		zap.Int64("payment_id", paymentID),
		zap.Int64("subscription_id", subscriptionID),
		zap.String("charge_id", sp.TelegramPaymentChargeID),
	)
	return "paid", nil
}

// handleRefund — Telegram вернул Stars. Отменяем подписку + DisableVPNUser.
func (s *PaymentService) handleRefund(ctx context.Context, rp *tgRefundedPayment) (string, error) {
	if err := s.repo.MarkRefunded(ctx, rp.TelegramPaymentChargeID); err != nil {
		return "refund_failed", err
	}

	// Находим payment чтобы знать user_id → подписку.
	p, err := s.repo.GetByExternalID(ctx, rp.TelegramPaymentChargeID)
	if err != nil {
		return "refund_failed", err
	}

	// Отменить активную подписку (find сначала).
	active, err := s.sub.GetActiveSubscription(ctx, &subpb.GetActiveSubscriptionRequest{UserId: p.UserID})
	if err == nil && active.GetHasActive() && active.GetSubscription() != nil {
		if _, cerr := s.sub.CancelSubscription(ctx, &subpb.CancelSubscriptionRequest{
			SubscriptionId: active.GetSubscription().GetId(),
		}); cerr != nil {
			s.log.Error("CancelSubscription failed during refund",
				zap.Int64("payment_id", p.ID), zap.Error(cerr))
		}
	}

	// Физически удалить юзера из Xray.
	if _, derr := s.vpn.DisableVPNUser(ctx, &vpnpb.DisableVPNUserRequest{UserId: p.UserID}); derr != nil {
		s.log.Error("DisableVPNUser failed during refund",
			zap.Int64("payment_id", p.ID), zap.Error(derr))
	}

	s.log.Info("payment refunded",
		zap.Int64("payment_id", p.ID),
		zap.String("charge_id", rp.TelegramPaymentChargeID),
	)
	return "refunded", nil
}

// GetPayment / ListByUser — простые геттеры.
func (s *PaymentService) GetPayment(ctx context.Context, id int64) (*model.Payment, error) {
	return s.repo.GetByID(ctx, id)
}
func (s *PaymentService) ListByUser(ctx context.Context, userID int64, limit, offset int32) ([]*model.Payment, error) {
	return s.repo.ListByUser(ctx, userID, limit, offset)
}

// --- Telegram update payload (минимальный парсинг) ---
//
// Документация: https://core.telegram.org/bots/api#update
// Парсим только то что нужно для Stars.

type tgUpdate struct {
	UpdateID         int64                `json:"update_id"`
	Message          *tgMessage           `json:"message,omitempty"`
	PreCheckoutQuery *tgPreCheckoutQuery  `json:"pre_checkout_query,omitempty"`
}

type tgMessage struct {
	From              tgUser              `json:"from"`
	SuccessfulPayment *tgSuccessfulPayment `json:"successful_payment,omitempty"`
	RefundedPayment   *tgRefundedPayment   `json:"refunded_payment,omitempty"`
}

type tgUser struct {
	ID int64 `json:"id"` // telegram_id
}

type tgPreCheckoutQuery struct {
	ID             string `json:"id"`
	InvoicePayload string `json:"invoice_payload"`
	Currency       string `json:"currency"`
	TotalAmount    int32  `json:"total_amount"`
}

type tgSuccessfulPayment struct {
	Currency                string `json:"currency"`
	TotalAmount             int32  `json:"total_amount"`
	InvoicePayload          string `json:"invoice_payload"`
	TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
	ProviderPaymentChargeID string `json:"provider_payment_charge_id"`
}

type tgRefundedPayment struct {
	Currency                string `json:"currency"`
	TotalAmount             int32  `json:"total_amount"`
	InvoicePayload          string `json:"invoice_payload"`
	TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
}
