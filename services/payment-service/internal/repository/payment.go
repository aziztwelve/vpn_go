package repository

import (
	"context"
	"errors"
	"fmt"

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
		INSERT INTO payments (user_id, plan_id, max_devices, amount_stars, status, provider, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at
	`
	var id int64
	err := r.db.QueryRow(ctx, q,
		p.UserID, p.PlanID, p.MaxDevices, p.AmountStars, model.StatusPending, p.Provider, p.Metadata,
	).Scan(&id, &p.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("insert payment: %w", err)
	}
	p.ID = id
	return id, nil
}

// MarkPaid атомарно меняет pending → paid c external_id и metadata.
func (r *PaymentRepository) MarkPaid(ctx context.Context, paymentID int64, metadata map[string]string) error {
	const q = `
		UPDATE payments
		SET status = 'paid', metadata = $2, paid_at = NOW()
		WHERE id = $1 AND status = 'pending'
	`
	tag, err := r.db.Exec(ctx, q, paymentID, metadata)
	if err != nil {
		return fmt.Errorf("mark paid: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Запись уже не pending (дубликат webhook'а)
		return nil
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
		SELECT id, user_id, plan_id, max_devices, amount_stars, status, external_id,
		       provider, metadata, created_at, paid_at
		FROM payments WHERE external_id = $1
	`
	p := &model.Payment{}
	err := r.db.QueryRow(ctx, q, externalID).Scan(
		&p.ID, &p.UserID, &p.PlanID, &p.MaxDevices, &p.AmountStars, &p.Status, &p.ExternalID,
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
		SELECT id, user_id, plan_id, max_devices, amount_stars, status, external_id,
		       provider, metadata, created_at, paid_at
		FROM payments WHERE id = $1
	`
	p := &model.Payment{}
	err := r.db.QueryRow(ctx, q, id).Scan(
		&p.ID, &p.UserID, &p.PlanID, &p.MaxDevices, &p.AmountStars, &p.Status, &p.ExternalID,
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

func (r *PaymentRepository) ListByUser(ctx context.Context, userID int64, limit, offset int32) ([]*model.Payment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const q = `
		SELECT id, user_id, plan_id, max_devices, amount_stars, status, external_id,
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
			&p.ID, &p.UserID, &p.PlanID, &p.MaxDevices, &p.AmountStars, &p.Status, &p.ExternalID,
			&p.Provider, &p.Metadata, &p.CreatedAt, &p.PaidAt,
		); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
