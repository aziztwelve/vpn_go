package provider

import (
	"context"
	"time"
)

// PaymentProvider — интерфейс для всех платёжных провайдеров.
// Следуя SOLID принципам, каждый провайдер реализует этот интерфейс независимо.
type PaymentProvider interface {
	// CreateInvoice создаёт платёж и возвращает ссылку для оплаты.
	CreateInvoice(ctx context.Context, req *CreateInvoiceRequest) (*Invoice, error)

	// GetPaymentStatus проверяет статус платежа по внешнему ID.
	GetPaymentStatus(ctx context.Context, externalID string) (*PaymentStatus, error)

	// HandleWebhook обрабатывает webhook от провайдера.
	// Возвращает событие с информацией о платеже.
	HandleWebhook(ctx context.Context, payload []byte, signature string) (*WebhookEvent, error)

	// ValidateWebhook проверяет подпись webhook.
	ValidateWebhook(payload []byte, signature string) error

	// Name возвращает имя провайдера (telegram_stars, yoomoney, yookassa).
	Name() string
}

// CreateInvoiceRequest — запрос на создание платежа.
type CreateInvoiceRequest struct {
	UserID      int64
	PlanID      int32
	MaxDevices  int32
	AmountStars int32              // для Telegram Stars
	AmountRUB   float64            // для YooMoney/ЮKassa
	Currency    string             // RUB, USD, EUR
	Description string             // описание платежа
	Metadata    map[string]string  // дополнительные данные
}

// Invoice — результат создания платежа.
type Invoice struct {
	ExternalID  string      // ID в системе провайдера
	InvoiceLink string      // URL для оплаты
	Amount      interface{} // Stars (int) или рубли (float64)
	Currency    string      // XTR (Stars), RUB, USD
	ExpiresAt   time.Time   // когда истекает
}

// PaymentStatus — статус платежа.
type PaymentStatus struct {
	ExternalID string
	Status     string // pending, paid, cancelled, failed
	PaidAt     *time.Time
}

// WebhookEvent — событие от webhook.
type WebhookEvent struct {
	ExternalID string            // ID платежа в системе провайдера
	Status     string            // paid, cancelled, failed
	UserID     int64             // ID пользователя
	PlanID     int32             // ID тарифа
	MaxDevices int32             // количество устройств
	Metadata   map[string]string // дополнительные данные
}

// ProviderError — ошибка провайдера.
type ProviderError struct {
	Provider string
	Code     string
	Message  string
	Err      error
}

func (e *ProviderError) Error() string {
	if e.Err != nil {
		return e.Provider + ": " + e.Message + ": " + e.Err.Error()
	}
	return e.Provider + ": " + e.Message
}

func (e *ProviderError) Unwrap() error {
	return e.Err
}
