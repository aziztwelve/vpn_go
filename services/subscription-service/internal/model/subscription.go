package model

import "time"

type SubscriptionPlan struct {
	ID           int32
	Name         string
	DurationDays int32
	MaxDevices   int32
	BasePrice    float64
	IsActive     bool
	PriceStars   int32
}

type DeviceAddonPricing struct {
	ID         int32
	PlanID     int32
	MaxDevices int32
	Price      float64
	PriceStars int32
	PlanName   string // JOINed for convenience
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
	StatusExpired   = "expired"
	StatusCancelled = "cancelled"
)
