package yoomoney

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vpn/payment-service/internal/provider"
	"go.uber.org/zap"
)

const (
	apiURL = "https://yoomoney.ru/api"
)

// YooMoneyProvider — провайдер для YooMoney (бывший Яндекс.Деньги).
type YooMoneyProvider struct {
	walletID     string // номер кошелька получателя
	secretKey    string // секретный ключ для проверки уведомлений
	returnURL    string // URL для возврата после оплаты
	notifyURL    string // URL для уведомлений
	client       *http.Client
	logger       *zap.Logger
}

// NewProvider создаёт новый YooMoney провайдер.
func NewProvider(walletID, secretKey, returnURL, notifyURL string, logger *zap.Logger) *YooMoneyProvider {
	return &YooMoneyProvider{
		walletID:  walletID,
		secretKey: secretKey,
		returnURL: returnURL,
		notifyURL: notifyURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// Name возвращает имя провайдера.
func (p *YooMoneyProvider) Name() string {
	return "yoomoney"
}

// CreateInvoice создаёт платёж через YooMoney.
// YooMoney использует форму оплаты, а не API для создания платежа.
func (p *YooMoneyProvider) CreateInvoice(ctx context.Context, req *provider.CreateInvoiceRequest) (*provider.Invoice, error) {
	if req.AmountRUB <= 0 {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_amount",
			Message:  fmt.Sprintf("amount_rub must be > 0, got %.2f", req.AmountRUB),
		}
	}

	// YooMoney использует форму оплаты с параметрами
	// Генерируем уникальный label для идентификации платежа
	// Формат: payment_{user_id}_{plan_id}_{max_devices}_{timestamp}
	label := fmt.Sprintf("payment_%d_%d_%d_%d", req.UserID, req.PlanID, req.MaxDevices, time.Now().Unix())

	// Формируем URL для оплаты
	params := url.Values{}
	params.Set("receiver", p.walletID)
	params.Set("quickpay-form", "shop")
	params.Set("targets", req.Description)
	params.Set("paymentType", "AC") // AC = банковская карта
	params.Set("sum", fmt.Sprintf("%.2f", req.AmountRUB))
	params.Set("label", label)
	
	if p.returnURL != "" {
		params.Set("successURL", p.returnURL)
	}

	invoiceLink := "https://yoomoney.ru/quickpay/confirm.xml?" + params.Encode()

	p.logger.Info("yoomoney invoice created",
		zap.Int64("user_id", req.UserID),
		zap.Int32("plan_id", req.PlanID),
		zap.Float64("amount_rub", req.AmountRUB),
		zap.String("label", label),
	)

	return &provider.Invoice{
		ExternalID:  label, // используем label как external_id
		InvoiceLink: invoiceLink,
		Amount:      req.AmountRUB,
		Currency:    "RUB",
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}, nil
}

// GetPaymentStatus проверяет статус платежа через API.
func (p *YooMoneyProvider) GetPaymentStatus(ctx context.Context, externalID string) (*provider.PaymentStatus, error) {
	// YooMoney не предоставляет публичный API для проверки статуса
	// Статус приходит только через уведомления (notifications)
	return nil, &provider.ProviderError{
		Provider: p.Name(),
		Code:     "not_supported",
		Message:  "yoomoney does not support status polling, use notifications",
	}
}

// HandleWebhook обрабатывает уведомление от YooMoney.
func (p *YooMoneyProvider) HandleWebhook(ctx context.Context, payload []byte, signature string) (*provider.WebhookEvent, error) {
	// Парсим form data
	values, err := url.ParseQuery(string(payload))
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_payload",
			Message:  "failed to parse form data",
			Err:      err,
		}
	}

	// Извлекаем параметры
	notificationType := values.Get("notification_type")
	operationID := values.Get("operation_id")
	amount := values.Get("amount")
	currency := values.Get("currency")
	datetime := values.Get("datetime")
	sender := values.Get("sender")
	codepro := values.Get("codepro")
	label := values.Get("label")
	sha1Hash := values.Get("sha1_hash")

	// Проверяем что это уведомление о платеже
	if notificationType != "p2p-incoming" {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "unsupported_notification",
			Message:  fmt.Sprintf("unsupported notification type: %s", notificationType),
		}
	}

	// Проверяем подпись
	expectedHash := p.calculateHash(notificationType, operationID, amount, currency, datetime, sender, codepro, p.secretKey, label)
	if sha1Hash != expectedHash {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_signature",
			Message:  "webhook signature validation failed",
		}
	}

	// Парсим label для получения данных платежа
	// Формат: payment_{user_id}_{plan_id}_{max_devices}_{timestamp}
	parts := strings.Split(label, "_")
	if len(parts) != 5 || parts[0] != "payment" {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_label",
			Message:  fmt.Sprintf("invalid label format: %s", label),
		}
	}

	userID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_label",
			Message:  "failed to parse user_id from label",
			Err:      err,
		}
	}

	planID, err := strconv.ParseInt(parts[2], 10, 32)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_label",
			Message:  "failed to parse plan_id from label",
			Err:      err,
		}
	}

	maxDevices, err := strconv.ParseInt(parts[3], 10, 32)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_label",
			Message:  "failed to parse max_devices from label",
			Err:      err,
		}
	}

	// Формируем metadata
	metadata := map[string]string{
		"operation_id": operationID,
		"amount":       amount,
		"currency":     currency,
		"datetime":     datetime,
		"sender":       sender,
		"label":        label,
	}

	p.logger.Info("yoomoney payment successful",
		zap.Int64("user_id", userID),
		zap.Int32("plan_id", int32(planID)),
		zap.String("operation_id", operationID),
		zap.String("amount", amount),
	)

	return &provider.WebhookEvent{
		ExternalID: operationID,
		Status:     "paid",
		UserID:     userID,
		PlanID:     int32(planID),
		MaxDevices: int32(maxDevices),
		Metadata:   metadata,
	}, nil
}

// ValidateWebhook проверяет подпись уведомления.
func (p *YooMoneyProvider) ValidateWebhook(payload []byte, signature string) error {
	// Валидация происходит в HandleWebhook
	return nil
}

// calculateHash вычисляет SHA-1 хеш для проверки подписи.
func (p *YooMoneyProvider) calculateHash(notificationType, operationID, amount, currency, datetime, sender, codepro, secret, label string) string {
	// Формат: notification_type&operation_id&amount&currency&datetime&sender&codepro&secret&label
	data := fmt.Sprintf("%s&%s&%s&%s&%s&%s&%s&%s&%s",
		notificationType, operationID, amount, currency, datetime, sender, codepro, secret, label)
	
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// YooMoneyNotification — структура уведомления от YooMoney.
type YooMoneyNotification struct {
	NotificationType string  `json:"notification_type"`
	OperationID      string  `json:"operation_id"`
	Amount           float64 `json:"amount,string"`
	Currency         string  `json:"currency"`
	Datetime         string  `json:"datetime"`
	Sender           string  `json:"sender"`
	Codepro          string  `json:"codepro"`
	Label            string  `json:"label"`
	Sha1Hash         string  `json:"sha1_hash"`
}
