package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vpn/payment-service/internal/model"
)

// ErrNotFound — платёж не существует.
var ErrNotFound = errors.New("payment not found")

type PaymentRepository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *PaymentRepository {
	return &PaymentRepository{db: db}
}

// CreatePending создаёт pending-запись. Возвращает ID.
func (r *PaymentRepository) CreatePending(ctx context.Context, p *model.Payment) (int64, error) {
	const q = `
		INSERT INTO payments (user_id, plan_id, max_devices, amount_stars, amount_rub, currency, status, provider, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at
	`
	var id int64
	err := r.db.QueryRow(ctx, q,
		p.UserID, p.PlanID, p.MaxDevices, p.AmountStars, p.AmountRUB, p.Currency, model.StatusPending, p.Provider, p.Metadata,
	).Scan(&id, &p.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("insert payment: %w", err)
	}
	p.ID = id
	return id, nil
}

// MarkPaidDBOnly — шаг 1 идемпотентного flow'а: pending → paid_db_only.
// Атомарно записывает metadata и paid_at. Если строка уже не pending
// (повторный webhook) — no-op без ошибки, вызывающий проверяет статус сам.
//
// См. service.handleSuccessfulPayment.
func (r *PaymentRepository) MarkPaidDBOnly(ctx context.Context, paymentID int64, metadata map[string]string) error {
	const q = `
		UPDATE payments
		SET status = 'paid_db_only', metadata = $2, paid_at = NOW()
		WHERE id = $1 AND status = 'pending'
	`
	if _, err := r.db.Exec(ctx, q, paymentID, metadata); err != nil {
		return fmt.Errorf("mark paid_db_only: %w", err)
	}
	return nil
}

// MarkSubscriptionDone — шаг 2: paid_db_only → paid_subscription_done.
// Сохраняет subscription_id в metadata.subscription_id (jsonb merge), чтобы
// при ретрае шага 3 знать какую подписку использовать.
//
// subscription_id хранится как строка — в model.Payment.Metadata это
// map[string]string, и pgx сканирует jsonb со смешанными типами как mixed
// scan (плохо). Унификация на string избегает type-mismatch'ей.
func (r *PaymentRepository) MarkSubscriptionDone(ctx context.Context, paymentID int64, subscriptionID int64) error {
	const q = `
		UPDATE payments
		SET status = 'paid_subscription_done',
		    metadata = COALESCE(metadata, '{}'::jsonb) || jsonb_build_object('subscription_id', $2::text)
		WHERE id = $1 AND status = 'paid_db_only'
	`
	// subscriptionID отдаём строкой: pgx не знает как закодировать int64
	// в text-type (OID 25), а jsonb_build_object('key', $2::text) ждёт
	// именно text. Конверсия на стороне Go — самое простое.
	subIDStr := strconv.FormatInt(subscriptionID, 10)
	if _, err := r.db.Exec(ctx, q, paymentID, subIDStr); err != nil {
		return fmt.Errorf("mark paid_subscription_done: %w", err)
	}
	return nil
}

// MarkComplete — шаг 3: paid_subscription_done → paid (финал).
// Это «зелёный свет» для Telegram-нотификации.
func (r *PaymentRepository) MarkComplete(ctx context.Context, paymentID int64) error {
	const q = `
		UPDATE payments
		SET status = 'paid'
		WHERE id = $1 AND status = 'paid_subscription_done'
	`
	if _, err := r.db.Exec(ctx, q, paymentID); err != nil {
		return fmt.Errorf("mark paid: %w", err)
	}
	return nil
}

// UpdateExternalID обновляет external_id для платежа.
func (r *PaymentRepository) UpdateExternalID(ctx context.Context, paymentID int64, externalID string) error {
	const q = `UPDATE payments SET external_id = $2 WHERE id = $1`
	_, err := r.db.Exec(ctx, q, paymentID, externalID)
	return err
}

// MarkCancelled переводит pending → cancelled.
func (r *PaymentRepository) MarkCancelled(ctx context.Context, paymentID int64) error {
	const q = `UPDATE payments SET status = 'cancelled' WHERE id = $1 AND status = 'pending'`
	_, err := r.db.Exec(ctx, q, paymentID)
	return err
}

// MarkFailed переводит pending → failed.
func (r *PaymentRepository) MarkFailed(ctx context.Context, paymentID int64) error {
	const q = `UPDATE payments SET status = 'failed' WHERE id = $1 AND status = 'pending'`
	_, err := r.db.Exec(ctx, q, paymentID)
	return err
}

// GetByExternalID — для идемпотентной проверки "этот payment_charge_id уже обработан?".
func (r *PaymentRepository) GetByExternalID(ctx context.Context, externalID string) (*model.Payment, error) {
	const q = `
		SELECT id, user_id, plan_id, max_devices, amount_stars, amount_rub, currency, status, external_id,
		       provider, metadata, created_at, paid_at
		FROM payments WHERE external_id = $1
	`
	p := &model.Payment{}
	err := r.db.QueryRow(ctx, q, externalID).Scan(
		&p.ID, &p.UserID, &p.PlanID, &p.MaxDevices, &p.AmountStars, &p.AmountRUB, &p.Currency, &p.Status, &p.ExternalID,
		&p.Provider, &p.Metadata, &p.CreatedAt, &p.PaidAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get by external_id: %w", err)
	}
	return p, nil
}

func (r *PaymentRepository) GetByID(ctx context.Context, id int64) (*model.Payment, error) {
	const q = `
		SELECT id, user_id, plan_id, max_devices, amount_stars, amount_rub, currency, status, external_id,
		       provider, metadata, created_at, paid_at
		FROM payments WHERE id = $1
	`
	p := &model.Payment{}
	err := r.db.QueryRow(ctx, q, id).Scan(
		&p.ID, &p.UserID, &p.PlanID, &p.MaxDevices, &p.AmountStars, &p.AmountRUB, &p.Currency, &p.Status, &p.ExternalID,
		&p.Provider, &p.Metadata, &p.CreatedAt, &p.PaidAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get by id: %w", err)
	}
	return p, nil
}

// MarkRefunded переводит paid → refunded.
func (r *PaymentRepository) MarkRefunded(ctx context.Context, externalID string) error {
	const q = `UPDATE payments SET status = 'refunded' WHERE external_id = $1 AND status = 'paid'`
	_, err := r.db.Exec(ctx, q, externalID)
	return err
}

// ListStuckPaid возвращает платежи, зависшие в промежуточных статусах
// (paid_db_only / paid_subscription_done) дольше staleAfter. Используется
// sentinel cron'ом для добивания флоу при сбое webhook-handler'а.
//
// Для эффективности в WHERE используем только status и paid_at (есть индекс
// idx_payments_status). Лимит — защита от резкого скачка (если их вдруг
// тысячи — обработаем порциями).
func (r *PaymentRepository) ListStuckPaid(ctx context.Context, staleAfter time.Duration, limit int32) ([]*model.Payment, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const q = `
		SELECT id, user_id, plan_id, max_devices, amount_stars, amount_rub, currency, status, external_id,
		       provider, metadata, created_at, paid_at
		FROM payments
		WHERE status IN ('paid_db_only', 'paid_subscription_done')
		  AND paid_at < NOW() - ($1 || ' seconds')::interval
		ORDER BY paid_at ASC
		LIMIT $2
	`
	rows, err := r.db.Query(ctx, q, fmt.Sprintf("%d", int(staleAfter.Seconds())), limit)
	if err != nil {
		return nil, fmt.Errorf("list stuck paid: %w", err)
	}
	defer rows.Close()

	var out []*model.Payment
	for rows.Next() {
		p := &model.Payment{}
		if err := rows.Scan(
			&p.ID, &p.UserID, &p.PlanID, &p.MaxDevices, &p.AmountStars, &p.AmountRUB, &p.Currency, &p.Status, &p.ExternalID,
			&p.Provider, &p.Metadata, &p.CreatedAt, &p.PaidAt,
		); err != nil {
			return nil, fmt.Errorf("scan stuck paid: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *PaymentRepository) ListByUser(ctx context.Context, userID int64, limit, offset int32) ([]*model.Payment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const q = `
		SELECT id, user_id, plan_id, max_devices, amount_stars, amount_rub, currency, status, external_id,
		       provider, metadata, created_at, paid_at
		FROM payments WHERE user_id = $1
		ORDER BY created_at DESC LIMIT $2 OFFSET $3
	`
	rows, err := r.db.Query(ctx, q, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list by user: %w", err)
	}
	defer rows.Close()

	var out []*model.Payment
	for rows.Next() {
		p := &model.Payment{}
		if err := rows.Scan(
			&p.ID, &p.UserID, &p.PlanID, &p.MaxDevices, &p.AmountStars, &p.AmountRUB, &p.Currency, &p.Status, &p.ExternalID,
			&p.Provider, &p.Metadata, &p.CreatedAt, &p.PaidAt,
		); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
