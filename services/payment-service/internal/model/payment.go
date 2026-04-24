package model

import "time"

// Status значения согласно CHECK-constraint миграции 001.
const (
	StatusPending   = "pending"
	StatusPaid      = "paid"
	StatusFailed    = "failed"
	StatusRefunded  = "refunded"
	StatusCancelled = "cancelled"
)

// Провайдеры платежей.
const (
	ProviderTelegramStars = "telegram_stars"
	ProviderYooMoney      = "yoomoney"
	ProviderYooKassa      = "yookassa"
)

type Payment struct {
	ID          int64
	UserID      int64
	PlanID      int32
	MaxDevices  int32
	AmountStars int32   // для Telegram Stars
	AmountRUB   float64 // для YooMoney/ЮKassa
	Currency    string  // XTR, RUB, USD
	Status      string
	// ExternalID — ID платежа в системе провайдера.
	// Для Telegram: telegram_payment_charge_id
	// Для YooMoney/YooKassa: payment_id
	ExternalID string
	Provider   string
	Metadata   map[string]string // JSONB, дополнительные данные
	CreatedAt  time.Time
	PaidAt     *time.Time
}
