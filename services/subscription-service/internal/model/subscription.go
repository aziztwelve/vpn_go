package model

import "time"

// SubscriptionPlan — тариф. Цены хранятся только в RUB (primary currency);
// цены в других валютах (Stars, USD, crypto) вычисляются на лету из
// currency_rates в API-слое, см. api/subscription.go.
type SubscriptionPlan struct {
	ID           int32
	Name         string
	DurationDays int32
	MaxDevices   int32
	BasePrice    float64 // RUB
	IsActive     bool
	IsTrial      bool
}

// DeviceAddonPricing — цена за (план × кол-во устройств). RUB only.
type DeviceAddonPricing struct {
	ID         int32
	PlanID     int32
	MaxDevices int32
	Price      float64 // RUB
	PlanName   string  // JOINed for convenience
}

type Subscription struct {
	ID         int64
	UserID     int64
	PlanID     int32
	PlanName   string
	MaxDevices int32
	TotalPrice float64
	StartedAt  time.Time
	ExpiresAt  time.Time
	Status     string
	CreatedAt  time.Time
}

const (
	StatusActive    = "active"
	StatusTrial     = "trial"
	StatusExpired   = "expired"
	StatusCancelled = "cancelled"
)
