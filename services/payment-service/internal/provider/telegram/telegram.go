package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/vpn/payment-service/internal/provider"
	"go.uber.org/zap"
)

const (
	apiURL = "https://api.telegram.org/bot%s/%s"
)

// TelegramProvider — провайдер для Telegram Stars.
type TelegramProvider struct {
	botToken string
	client   *http.Client
	logger   *zap.Logger
}

// NewProvider создаёт новый Telegram Stars провайдер.
func NewProvider(botToken string, logger *zap.Logger) (*TelegramProvider, error) {
	if botToken == "" {
		return nil, fmt.Errorf("bot token is required")
	}

	return &TelegramProvider{
		botToken: botToken,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}, nil
}

// Name возвращает имя провайдера.
func (p *TelegramProvider) Name() string {
	return "telegram_stars"
}

// CreateInvoice создаёт invoice для оплаты Stars.
func (p *TelegramProvider) CreateInvoice(ctx context.Context, req *provider.CreateInvoiceRequest) (*provider.Invoice, error) {
	if req.AmountStars <= 0 {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_amount",
			Message:  fmt.Sprintf("amount_stars must be > 0, got %d", req.AmountStars),
		}
	}

	// Формируем payload для идентификации платежа
	payload := map[string]interface{}{
		"user_id":     req.UserID,
		"plan_id":     req.PlanID,
		"max_devices": req.MaxDevices,
	}
	for k, v := range req.Metadata {
		payload[k] = v
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "marshal_error",
			Message:  "failed to marshal payload",
			Err:      err,
		}
	}

	// Создаём invoice через Telegram Bot API
	params := map[string]interface{}{
		"title":       req.Description,
		"description": req.Description,
		"payload":     string(payloadJSON),
		"currency":    "XTR",
		"prices": []map[string]interface{}{
			{
				"label":  req.Description,
				"amount": req.AmountStars,
			},
		},
	}

	var result struct {
		Ok     bool   `json:"ok"`
		Result string `json:"result"`
	}

	if err := p.callAPI(ctx, "createInvoiceLink", params, &result); err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "api_error",
			Message:  "failed to create invoice link",
			Err:      err,
		}
	}

	p.logger.Info("telegram invoice created",
		zap.Int64("user_id", req.UserID),
		zap.Int32("plan_id", req.PlanID),
		zap.Int32("amount_stars", req.AmountStars),
		zap.String("invoice_link", result.Result),
	)

	return &provider.Invoice{
		ExternalID:  "", // Telegram не даёт ID до оплаты
		InvoiceLink: result.Result,
		Amount:      req.AmountStars,
		Currency:    "XTR",
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}, nil
}

// GetPaymentStatus проверяет статус платежа.
func (p *TelegramProvider) GetPaymentStatus(ctx context.Context, externalID string) (*provider.PaymentStatus, error) {
	return nil, &provider.ProviderError{
		Provider: p.Name(),
		Code:     "not_supported",
		Message:  "telegram stars does not support status polling, use webhooks",
	}
}

// HandleWebhook обрабатывает webhook от Telegram.
func (p *TelegramProvider) HandleWebhook(ctx context.Context, payload []byte, signature string) (*provider.WebhookEvent, error) {
	var update struct {
		Message *struct {
			SuccessfulPayment *struct {
				Currency                string `json:"currency"`
				TotalAmount             int    `json:"total_amount"`
				InvoicePayload          string `json:"invoice_payload"`
				TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
				ProviderPaymentChargeID string `json:"provider_payment_charge_id"`
			} `json:"successful_payment"`
		} `json:"message"`
	}

	if err := json.Unmarshal(payload, &update); err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_payload",
			Message:  "failed to unmarshal telegram update",
			Err:      err,
		}
	}

	// Обрабатываем successful_payment
	if update.Message != nil && update.Message.SuccessfulPayment != nil {
		return p.handleSuccessfulPayment(update.Message.SuccessfulPayment)
	}

	return nil, &provider.ProviderError{
		Provider: p.Name(),
		Code:     "unknown_event",
		Message:  "unknown telegram update type",
	}
}

// ValidateWebhook проверяет подпись webhook.
func (p *TelegramProvider) ValidateWebhook(payload []byte, signature string) error {
	// Telegram не использует signature, валидация происходит на уровне Bot API
	return nil
}

// handleSuccessfulPayment обрабатывает успешный платёж.
func (p *TelegramProvider) handleSuccessfulPayment(payment *struct {
	Currency                string `json:"currency"`
	TotalAmount             int    `json:"total_amount"`
	InvoicePayload          string `json:"invoice_payload"`
	TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
	ProviderPaymentChargeID string `json:"provider_payment_charge_id"`
}) (*provider.WebhookEvent, error) {
	// Парсим payload
	var payloadData map[string]interface{}
	if err := json.Unmarshal([]byte(payment.InvoicePayload), &payloadData); err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_payload",
			Message:  "failed to unmarshal invoice payload",
			Err:      err,
		}
	}

	// Извлекаем данные
	userID, _ := payloadData["user_id"].(float64)
	planID, _ := payloadData["plan_id"].(float64)
	maxDevices, _ := payloadData["max_devices"].(float64)

	if userID == 0 || planID == 0 || maxDevices == 0 {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_payload",
			Message:  "missing required fields in payload",
		}
	}

	// Формируем metadata
	metadata := make(map[string]string)
	metadata["telegram_payment_charge_id"] = payment.TelegramPaymentChargeID
	metadata["provider_payment_charge_id"] = payment.ProviderPaymentChargeID
	metadata["total_amount"] = strconv.Itoa(payment.TotalAmount)
	metadata["currency"] = payment.Currency

	p.logger.Info("telegram payment successful",
		zap.Int64("user_id", int64(userID)),
		zap.Int32("plan_id", int32(planID)),
		zap.Int32("max_devices", int32(maxDevices)),
		zap.String("charge_id", payment.TelegramPaymentChargeID),
	)

	return &provider.WebhookEvent{
		ExternalID: payment.TelegramPaymentChargeID,
		Status:     "paid",
		UserID:     int64(userID),
		PlanID:     int32(planID),
		MaxDevices: int32(maxDevices),
		Metadata:   metadata,
	}, nil
}

// callAPI вызывает метод Telegram Bot API.
func (p *TelegramProvider) callAPI(ctx context.Context, method string, params interface{}, result interface{}) error {
	url := fmt.Sprintf(apiURL, p.botToken, method)

	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("api error: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	if err := json.Unmarshal(respBody, result); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}

	return nil
}
