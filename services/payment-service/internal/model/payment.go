package model

import "time"

// Status значения согласно CHECK-constraint миграции 001.
const (
	StatusPending  = "pending"
	StatusPaid     = "paid"
	StatusFailed   = "failed"
	StatusRefunded = "refunded"
)

const ProviderTelegramStars = "telegram_stars"

type Payment struct {
	ID          int64
	UserID      int64
	PlanID      int32
	MaxDevices  int32
	AmountStars int32
	Status      string
	// ExternalID — telegram_payment_charge_id. NULL для pending.
	ExternalID *string
	Provider   string
	Metadata   []byte // JSONB, raw
	CreatedAt  time.Time
	PaidAt     *time.Time
}
