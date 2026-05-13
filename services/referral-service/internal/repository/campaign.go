package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vpn/referral-service/internal/model"
)

// ErrCampaignNotFound — нет кампании с таким id/slug.
var ErrCampaignNotFound = errors.New("campaign not found")

// ErrCampaignSlugExists — slug уже занят (UNIQUE violation).
var ErrCampaignSlugExists = errors.New("campaign slug already exists")

// ErrCampaignPayoutExists — выплата по этому payment_id уже создана (идемпотентность).
var ErrCampaignPayoutExists = errors.New("campaign payout for this payment already exists")

// ─── CRUD кампаний ─────────────────────────────────────────────────

// CreateCampaign вставляет новую запись. Возвращает её с заполненными ID/CreatedAt.
// На UNIQUE-конфликте по slug возвращает ErrCampaignSlugExists.
func (r *Repository) CreateCampaign(ctx context.Context, c *model.Campaign) error {
	err := r.db.QueryRow(ctx, `
		INSERT INTO campaigns (slug, name, notes, partner_user_id, payout_percent, created_by, is_active, trial_duration_days)
		VALUES ($1, $2, $3, $4, $5, $6, TRUE, $7)
		RETURNING id, created_at, is_active
	`, c.Slug, c.Name, c.Notes, c.PartnerUserID, c.PayoutPercent, c.CreatedBy, c.TrialDurationDays).
		Scan(&c.ID, &c.CreatedAt, &c.IsActive)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrCampaignSlugExists
		}
		return fmt.Errorf("insert campaign: %w", err)
	}
	return nil
}

// GetCampaignByID — для GetCampaign / Update / Archive.
func (r *Repository) GetCampaignByID(ctx context.Context, id int64) (*model.Campaign, error) {
	c := &model.Campaign{}
	err := r.db.QueryRow(ctx, `
		SELECT id, slug, name, COALESCE(notes,''), partner_user_id, payout_percent,
		       is_active, created_by, created_at, archived_at, trial_duration_days
		FROM campaigns WHERE id = $1
	`, id).Scan(
		&c.ID, &c.Slug, &c.Name, &c.Notes, &c.PartnerUserID, &c.PayoutPercent,
		&c.IsActive, &c.CreatedBy, &c.CreatedAt, &c.ArchivedAt, &c.TrialDurationDays,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCampaignNotFound
		}
		return nil, fmt.Errorf("get campaign by id: %w", err)
	}
	return c, nil
}

// GetCampaignBySlug — для атрибуции (auth-service резолвит slug в campaign_id).
// Возвращает только активные (is_active=TRUE) — архивированные не атрибутируем.
func (r *Repository) GetCampaignBySlug(ctx context.Context, slug string) (*model.Campaign, error) {
	c := &model.Campaign{}
	err := r.db.QueryRow(ctx, `
		SELECT id, slug, name, COALESCE(notes,''), partner_user_id, payout_percent,
		       is_active, created_by, created_at, archived_at, trial_duration_days
		FROM campaigns WHERE slug = $1 AND is_active = TRUE
	`, slug).Scan(
		&c.ID, &c.Slug, &c.Name, &c.Notes, &c.PartnerUserID, &c.PayoutPercent,
		&c.IsActive, &c.CreatedBy, &c.CreatedAt, &c.ArchivedAt, &c.TrialDurationDays,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCampaignNotFound
		}
		return nil, fmt.Errorf("get campaign by slug: %w", err)
	}
	return c, nil
}

// UpdateCampaign — частичное обновление имени/notes/партнёра/процента/триал-override.
// nil-указатели = "не менять". Чтобы обнулить partner_user_id / payout_percent /
// trial_duration_days, сервис передаёт соответствующий clear*=true (см.
// service/campaign.go::UpdateCampaign).
//
// Slug менять нельзя — у блогера уже на руках deep-link'и.
func (r *Repository) UpdateCampaign(
	ctx context.Context,
	id int64,
	name, notes *string,
	partnerUserID *int64,
	payoutPercent *int32,
	clearPartner, clearPayout bool,
	trialDurationDays *int32, clearTrialDuration bool,
) error {
	// Собираем динамический UPDATE — pgx не любит "if-arg-then-set".
	args := []interface{}{}
	sets := []string{}
	add := func(col string, val interface{}) {
		args = append(args, val)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}

	if name != nil {
		add("name", *name)
	}
	if notes != nil {
		add("notes", *notes)
	}
	if clearPartner {
		add("partner_user_id", nil)
	} else if partnerUserID != nil {
		add("partner_user_id", *partnerUserID)
	}
	if clearPayout {
		add("payout_percent", nil)
	} else if payoutPercent != nil {
		add("payout_percent", *payoutPercent)
	}
	if clearTrialDuration {
		add("trial_duration_days", nil)
	} else if trialDurationDays != nil {
		add("trial_duration_days", *trialDurationDays)
	}

	if len(sets) == 0 {
		return nil // нечего менять
	}

	args = append(args, id)
	q := fmt.Sprintf("UPDATE campaigns SET %s WHERE id = $%d",
		joinComma(sets), len(args))

	tag, err := r.db.Exec(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("update campaign: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCampaignNotFound
	}
	return nil
}

// ArchiveCampaign — soft-delete. Архивированные кампании не отдают новые
// атрибуции (GetCampaignBySlug фильтрует по is_active).
func (r *Repository) ArchiveCampaign(ctx context.Context, id int64) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE campaigns
		SET is_active = FALSE, archived_at = NOW()
		WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("archive campaign: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCampaignNotFound
	}
	return nil
}

// ListCampaigns — пагинированный список. includeArchived=false → только active.
func (r *Repository) ListCampaigns(ctx context.Context, includeArchived bool, limit, offset int32) ([]*model.Campaign, int32, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	where := "WHERE 1=1"
	args := []interface{}{}
	if !includeArchived {
		where += " AND is_active = TRUE"
	}

	var total int32
	if err := r.db.QueryRow(ctx, "SELECT COUNT(*) FROM campaigns "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count campaigns: %w", err)
	}

	args = append(args, limit, offset)
	q := fmt.Sprintf(`
		SELECT id, slug, name, COALESCE(notes,''), partner_user_id, payout_percent,
		       is_active, created_by, created_at, archived_at, trial_duration_days
		FROM campaigns
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, len(args)-1, len(args))

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list campaigns: %w", err)
	}
	defer rows.Close()

	out := []*model.Campaign{}
	for rows.Next() {
		c := &model.Campaign{}
		if err := rows.Scan(
			&c.ID, &c.Slug, &c.Name, &c.Notes, &c.PartnerUserID, &c.PayoutPercent,
			&c.IsActive, &c.CreatedBy, &c.CreatedAt, &c.ArchivedAt, &c.TrialDurationDays,
		); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, nil
}

// ─── Stats / Funnel ────────────────────────────────────────────────

// GetCampaignStats — единый запрос по всем шагам воронки + revenue + payouts.
// Период (from/to) опционален; нулевые значения = без границы.
//
// Считаем 5 шагов воронки за один round-trip — субзапросы по разным таблицам
// JOIN'ятся не нужны, агрегаты независимы. Производительность приемлема для
// сотен кампаний; на десятках тысяч стоит думать о matview'ах.
func (r *Repository) GetCampaignStats(ctx context.Context, campaignID int64, from, to time.Time) (*model.CampaignStats, error) {
	// Преобразуем "нулевое" время в NULL для SQL — COALESCE-логика.
	var fromArg, toArg interface{}
	if !from.IsZero() {
		fromArg = from
	}
	if !to.IsZero() {
		toArg = to
	}

	const q = `
WITH params AS (
    SELECT $1::bigint AS cid, $2::timestamptz AS dt_from, $3::timestamptz AS dt_to
),
starts AS (
    SELECT COUNT(*)::int AS n
    FROM bot_starts, params
    WHERE bot_starts.campaign_id = params.cid
      AND (params.dt_from IS NULL OR bot_starts.started_at >= params.dt_from)
      AND (params.dt_to   IS NULL OR bot_starts.started_at <  params.dt_to)
),
opened AS (
    SELECT COUNT(*)::int AS n
    FROM user_attribution ua, params
    WHERE ua.campaign_id = params.cid
      AND (params.dt_from IS NULL OR ua.attributed_at >= params.dt_from)
      AND (params.dt_to   IS NULL OR ua.attributed_at <  params.dt_to)
),
trial_users AS (
    -- Активация = появление любой подписки (trial или платной) у атрибутированного юзера.
    -- DISTINCT user_id чтобы не считать продления.
    SELECT COUNT(DISTINCT s.user_id)::int AS n
    FROM subscriptions s
    JOIN user_attribution ua ON ua.user_id = s.user_id
    JOIN params ON ua.campaign_id = params.cid
    WHERE (params.dt_from IS NULL OR s.created_at >= params.dt_from)
      AND (params.dt_to   IS NULL OR s.created_at <  params.dt_to)
),
paid AS (
    -- status='paid' — терминальный успешный стейт (см. payments-service).
    -- amount_rub — сумма в рублях (revenue в CampaignStats.RevenueRUB).
    -- paid_at — момент поступления денег (индексирован).
    SELECT
      COUNT(DISTINCT p.user_id)::int                   AS n,
      COALESCE(SUM(p.amount_rub), 0)::float8           AS revenue
    FROM payments p
    JOIN user_attribution ua ON ua.user_id = p.user_id
    JOIN params ON ua.campaign_id = params.cid
    WHERE p.status = 'paid'
      AND (params.dt_from IS NULL OR p.paid_at >= params.dt_from)
      AND (params.dt_to   IS NULL OR p.paid_at <  params.dt_to)
),
payouts AS (
    SELECT COALESCE(SUM(amount), 0)::float8 AS total
    FROM campaign_payouts cp, params
    WHERE cp.campaign_id = params.cid
      AND (params.dt_from IS NULL OR cp.created_at >= params.dt_from)
      AND (params.dt_to   IS NULL OR cp.created_at <  params.dt_to)
)
SELECT
    starts.n, opened.n, trial_users.n,
    paid.n, paid.revenue,
    payouts.total
FROM starts, opened, trial_users, paid, payouts
`
	st := &model.CampaignStats{CampaignID: campaignID, From: from, To: to}
	if err := r.db.QueryRow(ctx, q, campaignID, fromArg, toArg).Scan(
		&st.Starts, &st.OpenedApp, &st.TrialActivated,
		&st.PaidUsers, &st.RevenueRUB,
		&st.PartnerPayoutsRUB,
	); err != nil {
		return nil, fmt.Errorf("get campaign stats: %w", err)
	}
	return st, nil
}

// ─── Campaign Payouts (для ApplyBonus) ─────────────────────────────

// CampaignAttribution — выборка нужная при ApplyBonus: campaign_id + partner_user_id
// + payout_percent. Если у юзера нет атрибуции, или у кампании нет выплат —
// возвращает nil без ошибки.
type CampaignAttribution struct {
	CampaignID    int64
	PartnerUserID int64
	PayoutPercent int32
}

func (r *Repository) GetCampaignAttribution(ctx context.Context, userID int64) (*CampaignAttribution, error) {
	var a CampaignAttribution
	var partnerPtr *int64
	var percentPtr *int32
	err := r.db.QueryRow(ctx, `
		SELECT c.id, c.partner_user_id, c.payout_percent
		FROM user_attribution ua
		JOIN campaigns c ON c.id = ua.campaign_id
		WHERE ua.user_id = $1
	`, userID).Scan(&a.CampaignID, &partnerPtr, &percentPtr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get campaign attribution: %w", err)
	}
	// Если выплат нет (NULL) — возвращаем nil (атрибуция есть, но выплачивать нечего).
	if partnerPtr == nil || percentPtr == nil {
		return nil, nil
	}
	a.PartnerUserID = *partnerPtr
	a.PayoutPercent = *percentPtr
	return &a, nil
}

// CreateCampaignPayout — записывает выплату по кампании. UNIQUE по payment_id
// гарантирует идемпотентность при retry webhook'ов оплат.
func (r *Repository) CreateCampaignPayout(ctx context.Context, campaignID, partnerID, invitedID, paymentID int64, amount float64) (int64, error) {
	var id int64
	err := r.db.QueryRow(ctx, `
		INSERT INTO campaign_payouts
		    (campaign_id, partner_user_id, invited_user_id, payment_id, amount, is_applied)
		VALUES ($1, $2, $3, $4, $5, FALSE)
		RETURNING id
	`, campaignID, partnerID, invitedID, paymentID, amount).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrCampaignPayoutExists
		}
		return 0, fmt.Errorf("insert campaign payout: %w", err)
	}
	return id, nil
}

// MarkCampaignPayoutApplied — после успешного зачисления на баланс.
func (r *Repository) MarkCampaignPayoutApplied(ctx context.Context, payoutID int64) error {
	_, err := r.db.Exec(ctx, `UPDATE campaign_payouts SET is_applied = TRUE WHERE id = $1`, payoutID)
	if err != nil {
		return fmt.Errorf("mark campaign payout applied: %w", err)
	}
	return nil
}

// ─── helpers ────────────────────────────────────────────────────────

// joinComma — простой strings.Join без import'а strings (он не используется в этом файле).
func joinComma(items []string) string {
	if len(items) == 0 {
		return ""
	}
	out := items[0]
	for _, s := range items[1:] {
		out += ", " + s
	}
	return out
}
