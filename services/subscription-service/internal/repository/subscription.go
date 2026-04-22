package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vpn/subscription-service/internal/model"
)

type SubscriptionRepository struct {
	db *pgxpool.Pool
}

func NewSubscriptionRepository(db *pgxpool.Pool) *SubscriptionRepository {
	return &SubscriptionRepository{db: db}
}

// Plans
func (r *SubscriptionRepository) ListPlans(ctx context.Context, activeOnly bool) ([]*model.SubscriptionPlan, error) {
	query := `SELECT id, name, duration_days, max_devices, base_price, is_active FROM subscription_plans`
	if activeOnly {
		query += ` WHERE is_active = true`
	}
	query += ` ORDER BY duration_days`

	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list plans: %w", err)
	}
	defer rows.Close()

	var plans []*model.SubscriptionPlan
	for rows.Next() {
		plan := &model.SubscriptionPlan{}
		if err := rows.Scan(&plan.ID, &plan.Name, &plan.DurationDays, &plan.MaxDevices, &plan.BasePrice, &plan.IsActive); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}

	return plans, nil
}

func (r *SubscriptionRepository) GetDevicePricing(ctx context.Context, planID int32) ([]*model.DeviceAddonPricing, error) {
	query := `SELECT id, plan_id, max_devices, price FROM device_addon_pricing WHERE plan_id = $1 ORDER BY max_devices`

	rows, err := r.db.Query(ctx, query, planID)
	if err != nil {
		return nil, fmt.Errorf("failed to get device pricing: %w", err)
	}
	defer rows.Close()

	var prices []*model.DeviceAddonPricing
	for rows.Next() {
		price := &model.DeviceAddonPricing{}
		if err := rows.Scan(&price.ID, &price.PlanID, &price.MaxDevices, &price.Price); err != nil {
			return nil, err
		}
		prices = append(prices, price)
	}

	return prices, nil
}

// Subscriptions
func (r *SubscriptionRepository) CreateSubscription(ctx context.Context, userID int64, planID int32, maxDevices int32, totalPrice float64) (*model.Subscription, error) {
	query := `
		INSERT INTO subscriptions (user_id, plan_id, max_devices, total_price, started_at, expires_at, status)
		SELECT $1, $2, $3, $4, NOW(), NOW() + INTERVAL '1 day' * sp.duration_days, 'active'
		FROM subscription_plans sp
		WHERE sp.id = $2
		RETURNING id, user_id, plan_id, max_devices, total_price, started_at, expires_at, status, created_at
	`

	sub := &model.Subscription{}
	err := r.db.QueryRow(ctx, query, userID, planID, maxDevices, totalPrice).Scan(
		&sub.ID, &sub.UserID, &sub.PlanID, &sub.MaxDevices, &sub.TotalPrice,
		&sub.StartedAt, &sub.ExpiresAt, &sub.Status, &sub.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create subscription: %w", err)
	}

	// Get plan name
	_ = r.db.QueryRow(ctx, `SELECT name FROM subscription_plans WHERE id = $1`, planID).Scan(&sub.PlanName)

	return sub, nil
}

func (r *SubscriptionRepository) GetActiveSubscription(ctx context.Context, userID int64) (*model.Subscription, error) {
	query := `
		SELECT s.id, s.user_id, s.plan_id, sp.name, s.max_devices, s.total_price, 
		       s.started_at, s.expires_at, s.status, s.created_at
		FROM subscriptions s
		JOIN subscription_plans sp ON s.plan_id = sp.id
		WHERE s.user_id = $1 AND s.status = 'active' AND s.expires_at > NOW()
		ORDER BY s.expires_at DESC
		LIMIT 1
	`

	sub := &model.Subscription{}
	err := r.db.QueryRow(ctx, query, userID).Scan(
		&sub.ID, &sub.UserID, &sub.PlanID, &sub.PlanName, &sub.MaxDevices, &sub.TotalPrice,
		&sub.StartedAt, &sub.ExpiresAt, &sub.Status, &sub.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("no active subscription: %w", err)
	}

	return sub, nil
}

func (r *SubscriptionRepository) GetSubscriptionByID(ctx context.Context, subscriptionID int64) (*model.Subscription, error) {
	query := `
		SELECT s.id, s.user_id, s.plan_id, sp.name, s.max_devices, s.total_price,
		       s.started_at, s.expires_at, s.status, s.created_at
		FROM subscriptions s
		JOIN subscription_plans sp ON s.plan_id = sp.id
		WHERE s.id = $1
	`

	sub := &model.Subscription{}
	err := r.db.QueryRow(ctx, query, subscriptionID).Scan(
		&sub.ID, &sub.UserID, &sub.PlanID, &sub.PlanName, &sub.MaxDevices, &sub.TotalPrice,
		&sub.StartedAt, &sub.ExpiresAt, &sub.Status, &sub.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("subscription not found: %w", err)
	}

	return sub, nil
}

func (r *SubscriptionRepository) ExtendSubscription(ctx context.Context, subscriptionID int64, days int32) (*model.Subscription, error) {
	query := `
		UPDATE subscriptions
		SET expires_at = expires_at + INTERVAL '1 day' * $1
		WHERE id = $2
		RETURNING id, user_id, plan_id, max_devices, total_price, started_at, expires_at, status, created_at
	`

	sub := &model.Subscription{}
	err := r.db.QueryRow(ctx, query, days, subscriptionID).Scan(
		&sub.ID, &sub.UserID, &sub.PlanID, &sub.MaxDevices, &sub.TotalPrice,
		&sub.StartedAt, &sub.ExpiresAt, &sub.Status, &sub.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to extend subscription: %w", err)
	}

	// Get plan name
	_ = r.db.QueryRow(ctx, `SELECT name FROM subscription_plans WHERE id = $1`, sub.PlanID).Scan(&sub.PlanName)

	return sub, nil
}

func (r *SubscriptionRepository) CancelSubscription(ctx context.Context, subscriptionID int64) (*model.Subscription, error) {
	query := `
		UPDATE subscriptions
		SET status = 'cancelled'
		WHERE id = $1
		RETURNING id, user_id, plan_id, max_devices, total_price, started_at, expires_at, status, created_at
	`

	sub := &model.Subscription{}
	err := r.db.QueryRow(ctx, query, subscriptionID).Scan(
		&sub.ID, &sub.UserID, &sub.PlanID, &sub.MaxDevices, &sub.TotalPrice,
		&sub.StartedAt, &sub.ExpiresAt, &sub.Status, &sub.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to cancel subscription: %w", err)
	}

	// Get plan name
	_ = r.db.QueryRow(ctx, `SELECT name FROM subscription_plans WHERE id = $1`, sub.PlanID).Scan(&sub.PlanName)

	return sub, nil
}

func (r *SubscriptionRepository) GetSubscriptionHistory(ctx context.Context, userID int64) ([]*model.Subscription, error) {
	query := `
		SELECT s.id, s.user_id, s.plan_id, sp.name, s.max_devices, s.total_price,
		       s.started_at, s.expires_at, s.status, s.created_at
		FROM subscriptions s
		JOIN subscription_plans sp ON s.plan_id = sp.id
		WHERE s.user_id = $1
		ORDER BY s.created_at DESC
	`

	rows, err := r.db.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get subscription history: %w", err)
	}
	defer rows.Close()

	var subs []*model.Subscription
	for rows.Next() {
		sub := &model.Subscription{}
		if err := rows.Scan(&sub.ID, &sub.UserID, &sub.PlanID, &sub.PlanName, &sub.MaxDevices, &sub.TotalPrice,
			&sub.StartedAt, &sub.ExpiresAt, &sub.Status, &sub.CreatedAt); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}

	return subs, nil
}
