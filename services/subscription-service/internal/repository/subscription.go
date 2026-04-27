package repository

import (
	"context"
	"fmt"
	"time"

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
// ListPlans по дефолту скрывает триал-планы — они выдаются только через StartTrial,
// не покупаются. Если нужны все (напр. для админки) — parameter nop сейчас, можно
// расширить отдельным флагом.
func (r *SubscriptionRepository) ListPlans(ctx context.Context, activeOnly bool) ([]*model.SubscriptionPlan, error) {
	query := `SELECT id, name, duration_days, max_devices, base_price, is_active, is_trial FROM subscription_plans WHERE is_trial = false`
	if activeOnly {
		query += ` AND is_active = true`
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
		if err := rows.Scan(&plan.ID, &plan.Name, &plan.DurationDays, &plan.MaxDevices,
			&plan.BasePrice, &plan.IsActive, &plan.IsTrial); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}

	return plans, nil
}

// GetTrialPlan возвращает первый активный триал-план (обычно id=99).
func (r *SubscriptionRepository) GetTrialPlan(ctx context.Context) (*model.SubscriptionPlan, error) {
	const q = `
		SELECT id, name, duration_days, max_devices, base_price, is_active, is_trial
		FROM subscription_plans
		WHERE is_trial = true AND is_active = true
		ORDER BY id
		LIMIT 1
	`
	plan := &model.SubscriptionPlan{}
	err := r.db.QueryRow(ctx, q).Scan(&plan.ID, &plan.Name, &plan.DurationDays, &plan.MaxDevices,
		&plan.BasePrice, &plan.IsActive, &plan.IsTrial)
	if err != nil {
		return nil, fmt.Errorf("get trial plan: %w", err)
	}
	return plan, nil
}

func (r *SubscriptionRepository) GetDevicePricing(ctx context.Context, planID int32) ([]*model.DeviceAddonPricing, error) {
	query := `
		SELECT d.id, d.plan_id, d.max_devices, d.price, p.name
		FROM device_addon_pricing d
		JOIN subscription_plans p ON p.id = d.plan_id
		WHERE d.plan_id = $1
		ORDER BY d.max_devices
	`

	rows, err := r.db.Query(ctx, query, planID)
	if err != nil {
		return nil, fmt.Errorf("failed to get device pricing: %w", err)
	}
	defer rows.Close()

	var prices []*model.DeviceAddonPricing
	for rows.Next() {
		price := &model.DeviceAddonPricing{}
		if err := rows.Scan(&price.ID, &price.PlanID, &price.MaxDevices, &price.Price,
			&price.PlanName); err != nil {
			return nil, err
		}
		prices = append(prices, price)
	}

	return prices, nil
}

// GetRateToRub возвращает курс валюты к рублю.
// 1 unit валюты = <rate> RUB. Используется для конвертации цен при сборке
// proto-ответов (price_stars = ceil(base_price / rate_stars)).
//
// Для неизвестных валют возвращает ошибку — лучше сломаться явно чем показать
// нулевую цену.
func (r *SubscriptionRepository) GetRateToRub(ctx context.Context, currency string) (float64, error) {
	const q = `SELECT rate_to_rub FROM currency_rates WHERE currency = $1`
	var rate float64
	if err := r.db.QueryRow(ctx, q, currency).Scan(&rate); err != nil {
		return 0, fmt.Errorf("get rate for %s: %w", currency, err)
	}
	return rate, nil
}

// ExpireOverdueSubscriptions помечает active/trial-подписки с expires_at < NOW()
// как expired и возвращает user_id'ы чтобы cron мог дёрнуть vpn-service.DisableVPNUser.
// Триалы обрабатываются тем же механизмом — status='trial' → 'expired' по тому же cron'у.
func (r *SubscriptionRepository) ExpireOverdueSubscriptions(ctx context.Context) ([]int64, error) {
	const q = `
		UPDATE subscriptions
		SET status = 'expired'
		WHERE status IN ('active', 'trial') AND expires_at < NOW()
		RETURNING user_id
	`
	rows, err := r.db.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("expire overdue: %w", err)
	}
	defer rows.Close()

	var userIDs []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		userIDs = append(userIDs, uid)
	}
	return userIDs, nil
}

// Subscriptions
// CreateSubscription — upsert по user_id: если у юзера уже есть подписка
// (включая активный триал) — обновляет её in-place с суммированием дней.
//   - Активный триал → становится active, остаток триала + plan.duration_days
//   - Активная подписка → продлевается (expires_at + duration)
//   - Истёкшая / отменённая → expires_at = NOW() + duration (как новая)
//   - Нет подписки → INSERT
//
// Это позволяет payment-service просто дёрнуть CreateSubscription после оплаты
// без if/else логики вокруг существующей подписки.
func (r *SubscriptionRepository) CreateSubscription(ctx context.Context, userID int64, planID int32, maxDevices int32, totalPrice float64) (*model.Subscription, error) {
	// Одна транзакция: check existing + compute new expires_at + UPSERT.
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var durationDays int32
	if err := tx.QueryRow(ctx, `SELECT duration_days FROM subscription_plans WHERE id = $1`, planID).Scan(&durationDays); err != nil {
		return nil, fmt.Errorf("get plan duration: %w", err)
	}

	var existingID int64
	var existingExpires time.Time
	var existingStatus string
	err = tx.QueryRow(ctx, `
		SELECT id, expires_at, status
		FROM subscriptions
		WHERE user_id = $1
		ORDER BY expires_at DESC
		LIMIT 1
		FOR UPDATE
	`, userID).Scan(&existingID, &existingExpires, &existingStatus)

	sub := &model.Subscription{}
	if err != nil {
		// Нет существующей — создаём с нуля.
		err = tx.QueryRow(ctx, `
			INSERT INTO subscriptions (user_id, plan_id, max_devices, total_price, started_at, expires_at, status)
			VALUES ($1, $2, $3, $4, NOW(), NOW() + INTERVAL '1 day' * $5, 'active')
			RETURNING id, user_id, plan_id, max_devices, total_price, started_at, expires_at, status, created_at
		`, userID, planID, maxDevices, totalPrice, durationDays).Scan(
			&sub.ID, &sub.UserID, &sub.PlanID, &sub.MaxDevices, &sub.TotalPrice,
			&sub.StartedAt, &sub.ExpiresAt, &sub.Status, &sub.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("insert subscription: %w", err)
		}
	} else {
		// Есть существующая — продлеваем.
		// Если активная (active/trial) и ещё не истекла → суммируем с existingExpires
		// Если истекла/отменена → считаем от NOW()
		baseExpires := "NOW()"
		if (existingStatus == model.StatusActive || existingStatus == model.StatusTrial) && existingExpires.After(time.Now()) {
			baseExpires = "GREATEST(NOW(), expires_at)"
		}
		q := fmt.Sprintf(`
			UPDATE subscriptions
			SET plan_id = $2, max_devices = $3, total_price = $4,
			    expires_at = %s + INTERVAL '1 day' * $5,
			    status = 'active'
			WHERE id = $1
			RETURNING id, user_id, plan_id, max_devices, total_price, started_at, expires_at, status, created_at
		`, baseExpires)
		err = tx.QueryRow(ctx, q, existingID, planID, maxDevices, totalPrice, durationDays).Scan(
			&sub.ID, &sub.UserID, &sub.PlanID, &sub.MaxDevices, &sub.TotalPrice,
			&sub.StartedAt, &sub.ExpiresAt, &sub.Status, &sub.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("update subscription: %w", err)
		}
	}

	// Plan name — внутри той же транзакции чтобы избежать лишнего round-trip.
	_ = tx.QueryRow(ctx, `SELECT name FROM subscription_plans WHERE id = $1`, planID).Scan(&sub.PlanName)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return sub, nil
}

// StartTrialTx атомарно активирует триал новому юзеру.
// Возвращает (sub, alreadyUsed, err). alreadyUsed=true — trial_used_at
// уже стоит у этого юзера, новая подписка не создана.
func (r *SubscriptionRepository) StartTrialTx(ctx context.Context, userID int64, trialPlan *model.SubscriptionPlan) (*model.Subscription, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Lock юзера на время транзакции — защита от race между параллельными
	// /auth/validate одного и того же нового юзера.
	var trialUsedAt *time.Time
	if err := tx.QueryRow(ctx,
		`SELECT trial_used_at FROM users WHERE id = $1 FOR UPDATE`,
		userID,
	).Scan(&trialUsedAt); err != nil {
		return nil, false, fmt.Errorf("select user for update: %w", err)
	}
	if trialUsedAt != nil {
		return nil, true, nil
	}

	// Создаём trial-подписку.
	sub := &model.Subscription{}
	err = tx.QueryRow(ctx, `
		INSERT INTO subscriptions (user_id, plan_id, max_devices, total_price, started_at, expires_at, status)
		VALUES ($1, $2, $3, 0, NOW(), NOW() + INTERVAL '1 day' * $4, 'trial')
		RETURNING id, user_id, plan_id, max_devices, total_price, started_at, expires_at, status, created_at
	`, userID, trialPlan.ID, trialPlan.MaxDevices, trialPlan.DurationDays).Scan(
		&sub.ID, &sub.UserID, &sub.PlanID, &sub.MaxDevices, &sub.TotalPrice,
		&sub.StartedAt, &sub.ExpiresAt, &sub.Status, &sub.CreatedAt,
	)
	if err != nil {
		return nil, false, fmt.Errorf("insert trial sub: %w", err)
	}
	sub.PlanName = trialPlan.Name

	// Отмечаем в users что триал выдан.
	if _, err := tx.Exec(ctx,
		`UPDATE users SET trial_used_at = NOW() WHERE id = $1`,
		userID,
	); err != nil {
		return nil, false, fmt.Errorf("update trial_used_at: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit: %w", err)
	}

	return sub, false, nil
}

func (r *SubscriptionRepository) GetActiveSubscription(ctx context.Context, userID int64) (*model.Subscription, error) {
	// Триал считается активной подпиской — юзер с trial может пользоваться VPN.
	// userID здесь это внутренний id из таблицы users
	query := `
		SELECT s.id, s.user_id, s.plan_id, sp.name, s.max_devices, s.total_price, 
		       s.started_at, s.expires_at, s.status, s.created_at
		FROM subscriptions s
		JOIN subscription_plans sp ON s.plan_id = sp.id
		WHERE s.user_id = $1 AND s.status IN ('active', 'trial') AND s.expires_at > NOW()
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

// ClaimChannelBonusTx атомарно начисляет +3 дня к активной подписке за подписку на канал.
// Возвращает (sub, alreadyClaimed, noActiveSub, err).
// alreadyClaimed=true — users.channel_bonus_claimed уже true, бонус не начислен.
// noActiveSub=true — нет активной подписки для продления.
func (r *SubscriptionRepository) ClaimChannelBonusTx(ctx context.Context, userID int64) (*model.Subscription, bool, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, false, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Lock юзера и проверяем флаг channel_bonus_claimed
	var bonusClaimed bool
	if err := tx.QueryRow(ctx,
		`SELECT channel_bonus_claimed FROM users WHERE telegram_id = $1 FOR UPDATE`,
		userID,
	).Scan(&bonusClaimed); err != nil {
		return nil, false, false, fmt.Errorf("select user for update: %w", err)
	}
	if bonusClaimed {
		return nil, true, false, nil
	}

	// Получаем активную подписку
	var subID int64
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, expires_at
		FROM subscriptions
		WHERE user_id = $1 AND status IN ('active', 'trial') AND expires_at > NOW()
		ORDER BY expires_at DESC
		LIMIT 1
	`, userID).Scan(&subID, &expiresAt)
	if err != nil {
		// Нет активной подписки
		return nil, false, true, nil
	}

	// Продлеваем подписку на 3 дня
	newExpires := expiresAt.Add(3 * 24 * time.Hour)
	sub := &model.Subscription{}
	err = tx.QueryRow(ctx, `
		UPDATE subscriptions
		SET expires_at = $1
		WHERE id = $2
		RETURNING id, user_id, plan_id, max_devices, total_price, started_at, expires_at, status, created_at
	`, newExpires, subID).Scan(
		&sub.ID, &sub.UserID, &sub.PlanID, &sub.MaxDevices, &sub.TotalPrice,
		&sub.StartedAt, &sub.ExpiresAt, &sub.Status, &sub.CreatedAt,
	)
	if err != nil {
		return nil, false, false, fmt.Errorf("update subscription: %w", err)
	}

	// Получаем имя плана
	_ = tx.QueryRow(ctx, `SELECT name FROM subscription_plans WHERE id = $1`, sub.PlanID).Scan(&sub.PlanName)

	// Отмечаем что бонус получен
	if _, err := tx.Exec(ctx,
		`UPDATE users SET channel_bonus_claimed = true, channel_bonus_claimed_at = NOW() WHERE telegram_id = $1`,
		userID,
	); err != nil {
		return nil, false, false, fmt.Errorf("update channel_bonus_claimed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, false, fmt.Errorf("commit: %w", err)
	}

	return sub, false, false, nil
}
