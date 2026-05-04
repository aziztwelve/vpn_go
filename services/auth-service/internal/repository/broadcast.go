// Package repository — broadcast.go: retention-рассылки.
//
// Работает с двумя таблицами (см. миграцию 1777700000_add_broadcasts.up.sql):
//   - broadcast_drafts: черновик с snapshot'ом recipient_ids, ждёт approve
//   - broadcast_sends: per-recipient лог доставки + open/click трекинг
//
// Используется из service/retention_cron.go. HTTP-слой (admin handlers)
// и сам sender — Stage 3+ (см. docs/tasks/15-retention-campaigns.md).
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BroadcastRepository — доступ к broadcast_drafts и broadcast_sends + вспомогательные
// запросы по users/subscriptions/admin-идентификация (используемые RetentionCron'ом).
type BroadcastRepository struct {
	db *pgxpool.Pool
}

func NewBroadcastRepository(db *pgxpool.Pool) *BroadcastRepository {
	return &BroadcastRepository{db: db}
}

// ButtonConfig — элемент button_config (JSONB array). Соответствует структуре,
// ожидаемой Stage 3 BroadcastSender'ом (рендерит в InlineKeyboardButton).
type ButtonConfig struct {
	Text string `json:"text"`
	Type string `json:"type"` // "web_app" | "url" | "callback_data"
	URL  string `json:"url,omitempty"`
	Data string `json:"data,omitempty"` // для type=callback_data
}

// DraftInput — входные параметры для InsertBroadcastDraft. Сам список
// recipient_ids фиксируется как snapshot (если юзер сменит статус между
// генерацией и approve — он всё равно получит, as designed).
type DraftInput struct {
	SegmentKey    string
	Title         string
	BodyTemplate  string
	Buttons       []ButtonConfig
	RecipientIDs  []int64
}

// InsertBroadcastDraft создаёт черновик. Возвращает id для дальнейшей
// ссылки в notify-сообщении (кнопки с callback_data=bc_approve_<id>).
func (r *BroadcastRepository) InsertBroadcastDraft(ctx context.Context, in DraftInput) (int64, error) {
	btnJSON, err := json.Marshal(in.Buttons)
	if err != nil {
		return 0, fmt.Errorf("marshal buttons: %w", err)
	}
	if in.Buttons == nil {
		btnJSON = []byte("[]") // не NULL, не 'null' — пустой JSONB-массив
	}

	var id int64
	err = r.db.QueryRow(ctx, `
		INSERT INTO broadcast_drafts
		  (segment_key, title, body_template, button_config, recipient_ids, recipient_count)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`,
		in.SegmentKey, in.Title, in.BodyTemplate, btnJSON,
		in.RecipientIDs, len(in.RecipientIDs),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert broadcast draft: %w", err)
	}
	return id, nil
}

// ─── Segment-recipient селекторы ────────────────────────────────────────
//
// Все 4 сегмента — отдельные методы, а не один generic SelectByFilter с
// WHERE как параметром, чтобы фильтры оставались статичным SQL (защита
// от SQL-injection и читаемость). Overlap-защита:
//   1. Priority-ordering: trial_never_connected исключает последние 24h
//      (эти попадают в trial_ending_*). Между trial_ending_* и
//      paid_churn_risk overlap невозможен (разные s.status).
//   2. Глобальный rate-limit: юзер не получает второй retention-push в
//      течение 24h (NOT EXISTS broadcast_sends WHERE sent_at > NOW()-24h).
//   3. Per-segment дедуп 7 дней: не повторяем тот же сегмент чаще раза в
//      неделю (NOT EXISTS ... segment_key = ... AND sent_at > NOW()-7d).
//
// Все методы возвращают []int64 (users.id). RetentionCron дальше JOIN'ит
// их в recipient_ids и InsertBroadcastDraft.

// SelectTrialNeverConnected — триал ≥1ч назад, никогда не было трафика,
// трайл истекает НЕ раньше чем через 24ч (чтобы не пересекаться с
// trial_ending_idle). Cap limit — hard-cap на день, чтобы не взорваться
// при внезапном росте регистраций.
func (r *BroadcastRepository) SelectTrialNeverConnected(ctx context.Context, limit int) ([]int64, error) {
	return r.selectWithLimit(ctx, limit, `
		SELECT u.id
		FROM users u
		JOIN subscriptions s ON s.user_id = u.id
		WHERE s.status = 'trial'
		  AND s.started_at <= NOW() - INTERVAL '1 hour'
		  AND s.expires_at > NOW() + INTERVAL '24 hours'
		  AND u.first_connection_at IS NULL
		  AND u.is_banned = FALSE
		  AND NOT EXISTS (
		      SELECT 1 FROM broadcast_sends bs
		      WHERE bs.user_id = u.id
		        AND bs.status = 'sent'
		        AND bs.sent_at > NOW() - INTERVAL '24 hours'
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM broadcast_sends bs
		      JOIN broadcast_drafts bd ON bd.id = bs.broadcast_id
		      WHERE bs.user_id = u.id
		        AND bd.segment_key = 'trial_never_connected'
		        AND bs.sent_at > NOW() - INTERVAL '7 days'
		  )
		ORDER BY s.started_at ASC
	`)
}

// SelectTrialEndingIdle — триал кончается <24ч, за сутки не было трафика
// (либо никогда не было). Сильный churn-сигнал: юзер почти не попробовал
// и вот-вот истечёт.
func (r *BroadcastRepository) SelectTrialEndingIdle(ctx context.Context, limit int) ([]int64, error) {
	return r.selectWithLimit(ctx, limit, `
		SELECT u.id
		FROM users u
		JOIN subscriptions s ON s.user_id = u.id
		WHERE s.status = 'trial'
		  AND s.expires_at BETWEEN NOW() AND NOW() + INTERVAL '24 hours'
		  AND (u.last_traffic_at IS NULL OR u.last_traffic_at < NOW() - INTERVAL '24 hours')
		  AND u.is_banned = FALSE
		  AND NOT EXISTS (
		      SELECT 1 FROM broadcast_sends bs
		      WHERE bs.user_id = u.id
		        AND bs.status = 'sent'
		        AND bs.sent_at > NOW() - INTERVAL '24 hours'
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM broadcast_sends bs
		      JOIN broadcast_drafts bd ON bd.id = bs.broadcast_id
		      WHERE bs.user_id = u.id
		        AND bd.segment_key = 'trial_ending_idle'
		        AND bs.sent_at > NOW() - INTERVAL '7 days'
		  )
		ORDER BY s.expires_at ASC
	`)
}

// SelectTrialEndingActive — триал кончается <24ч, был трафик за сутки.
// Conversion-сигнал: юзер реально пользуется, стоит подтолкнуть к оплате.
func (r *BroadcastRepository) SelectTrialEndingActive(ctx context.Context, limit int) ([]int64, error) {
	return r.selectWithLimit(ctx, limit, `
		SELECT u.id
		FROM users u
		JOIN subscriptions s ON s.user_id = u.id
		WHERE s.status = 'trial'
		  AND s.expires_at BETWEEN NOW() AND NOW() + INTERVAL '24 hours'
		  AND u.last_traffic_at >= NOW() - INTERVAL '24 hours'
		  AND u.is_banned = FALSE
		  AND NOT EXISTS (
		      SELECT 1 FROM broadcast_sends bs
		      WHERE bs.user_id = u.id
		        AND bs.status = 'sent'
		        AND bs.sent_at > NOW() - INTERVAL '24 hours'
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM broadcast_sends bs
		      JOIN broadcast_drafts bd ON bd.id = bs.broadcast_id
		      WHERE bs.user_id = u.id
		        AND bd.segment_key = 'trial_ending_active'
		        AND bs.sent_at > NOW() - INTERVAL '7 days'
		  )
		ORDER BY s.expires_at ASC
	`)
}

// SelectPaidChurnRisk — active-подписка, трафика >3 дней нет, до конца
// подписки ещё >3 дней (т.е. не близится к обычному auto-expire-flow).
func (r *BroadcastRepository) SelectPaidChurnRisk(ctx context.Context, limit int) ([]int64, error) {
	return r.selectWithLimit(ctx, limit, `
		SELECT u.id
		FROM users u
		JOIN subscriptions s ON s.user_id = u.id
		WHERE s.status = 'active'
		  AND s.expires_at > NOW() + INTERVAL '3 days'
		  AND (u.last_traffic_at IS NULL OR u.last_traffic_at < NOW() - INTERVAL '3 days')
		  AND u.is_banned = FALSE
		  AND NOT EXISTS (
		      SELECT 1 FROM broadcast_sends bs
		      WHERE bs.user_id = u.id
		        AND bs.status = 'sent'
		        AND bs.sent_at > NOW() - INTERVAL '24 hours'
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM broadcast_sends bs
		      JOIN broadcast_drafts bd ON bd.id = bs.broadcast_id
		      WHERE bs.user_id = u.id
		        AND bd.segment_key = 'paid_churn_risk'
		        AND bs.sent_at > NOW() - INTERVAL '7 days'
		  )
		ORDER BY u.last_traffic_at ASC NULLS FIRST
	`)
}

// selectWithLimit — общая реализация «SELECT id + опциональный LIMIT».
// limit=0 трактуется как «без лимита» (для сегментов с DailyCap=0).
func (r *BroadcastRepository) selectWithLimit(ctx context.Context, limit int, q string) ([]int64, error) {
	if limit > 0 {
		q = q + "\nLIMIT " + fmt.Sprint(limit)
	}
	rows, err := r.db.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("select segment recipients: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ─── Admin-identification + user-details для notify ─────────────────────

// ListAdminTelegramIDs возвращает telegram_id всех юзеров с role='admin'.
// Используется RetentionCron'ом для персонализированной отправки превью.
// Если 0 — RetentionCron залогирует warn и пропустит notify (запуск
// всё равно прошёл — drafts созданы, их можно approve руками через SQL
// UPDATE пока Stage 5 не приехал).
func (r *BroadcastRepository) ListAdminTelegramIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.Query(ctx, `
		SELECT telegram_id FROM users WHERE role = 'admin' AND is_banned = FALSE
	`)
	if err != nil {
		return nil, fmt.Errorf("list admin telegram ids: %w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var tgID int64
		if err := rows.Scan(&tgID); err != nil {
			return nil, err
		}
		out = append(out, tgID)
	}
	return out, rows.Err()
}

// UserPreview — минимальное представление юзера для превью-шаблона.
type UserPreview struct {
	ID         int64
	TelegramID int64
	Username   string
	FirstName  string
}

// GetUserPreviews принимает set id и возвращает их данные для рендера
// превью ("Первый получатель: @username, Ivan, ..."). Порядок не
// гарантируется. Если слишком много — RetentionCron сам обрежет для
// notify-сообщения.
func (r *BroadcastRepository) GetUserPreviews(ctx context.Context, ids []int64) ([]UserPreview, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT id, telegram_id, COALESCE(username, ''), COALESCE(first_name, '')
		FROM users WHERE id = ANY($1::bigint[])
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("get user previews: %w", err)
	}
	defer rows.Close()

	var out []UserPreview
	for rows.Next() {
		var u UserPreview
		if err := rows.Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ─── Query-методы для будущих stages (админские ручки / sender) ──────────
// Не используются RetentionCron'ом, но логически принадлежат этому repo.
// Заведены сразу, чтобы Stage 3+ не плодил отдельные файлы.

// DraftSummary — метаинфо для списка в админке.
type DraftSummary struct {
	ID             int64
	SegmentKey     string
	Title          string
	RecipientCount int
	Status         string
	CreatedAt      time.Time
	SentAt         *time.Time
}

// Draft — полные данные draft'а для sender'а.
type Draft struct {
	ID             int64
	SegmentKey     string
	Title          string
	BodyTemplate   string
	ButtonConfig   []ButtonConfig
	RecipientIDs   []int64
	RecipientCount int
	Status         string
	CreatedAt      time.Time
	ApprovedAt     *time.Time
	ApprovedBy     *int64
	SentAt         *time.Time
}

// GetDraft читает draft со всеми полями. Используется sender'ом для
// итерации по recipient_ids и рендеринга шаблона.
func (r *BroadcastRepository) GetDraft(ctx context.Context, id int64) (*Draft, error) {
	var d Draft
	var btnRaw []byte
	err := r.db.QueryRow(ctx, `
		SELECT id, segment_key, title, body_template, button_config,
		       recipient_ids, recipient_count, status, created_at,
		       approved_at, approved_by, sent_at
		FROM broadcast_drafts WHERE id = $1
	`, id).Scan(
		&d.ID, &d.SegmentKey, &d.Title, &d.BodyTemplate, &btnRaw,
		&d.RecipientIDs, &d.RecipientCount, &d.Status, &d.CreatedAt,
		&d.ApprovedAt, &d.ApprovedBy, &d.SentAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get draft %d: %w", id, err)
	}
	if len(btnRaw) > 0 {
		if err := json.Unmarshal(btnRaw, &d.ButtonConfig); err != nil {
			return nil, fmt.Errorf("unmarshal buttons: %w", err)
		}
	}
	return &d, nil
}

// UpdateDraftStatus переводит draft в новый статус. approvedByUserID
// проставляется только при переходе в 'approved' — для остальных передаётся 0.
//
// Возвращает affected — фактическое число обновлённых строк (0 если
// статус не подошёл и UPDATE отфильтровался). Используется как
// идемпотентный предикат: если 0 — значит draft уже не в expectedFromStatus.
func (r *BroadcastRepository) UpdateDraftStatus(
	ctx context.Context,
	id int64,
	expectedFromStatus, newStatus string,
	approvedByUserID int64,
) (int64, error) {
	var query string
	var args []any
	switch newStatus {
	case "approved":
		query = `
			UPDATE broadcast_drafts
			SET status = $1, approved_at = NOW(), approved_by = $2
			WHERE id = $3 AND status = $4
		`
		args = []any{newStatus, approvedByUserID, id, expectedFromStatus}
	case "sent":
		query = `
			UPDATE broadcast_drafts
			SET status = $1, sent_at = NOW()
			WHERE id = $2 AND status = $3
		`
		args = []any{newStatus, id, expectedFromStatus}
	default:
		query = `
			UPDATE broadcast_drafts
			SET status = $1
			WHERE id = $2 AND status = $3
		`
		args = []any{newStatus, id, expectedFromStatus}
	}
	tag, err := r.db.Exec(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("update draft status %d → %s: %w", id, newStatus, err)
	}
	return tag.RowsAffected(), nil
}

// SendInput — одна строка лога доставки.
type SendInput struct {
	BroadcastID       int64
	UserID            int64
	TelegramMessageID *int64
	Status            string // pending | sent | blocked | failed
	ErrorCode         *int32
	ErrorMessage      string
}

// InsertSend пишет одну строку в broadcast_sends. Конфликты по UNIQUE
// (broadcast_id, user_id) НЕ обрабатываем — sender не должен отправлять
// дважды одному юзеру в рамках одной рассылки.
func (r *BroadcastRepository) InsertSend(ctx context.Context, in SendInput) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO broadcast_sends
		  (broadcast_id, user_id, telegram_message_id, status,
		   error_code, error_message, sent_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
	`,
		in.BroadcastID, in.UserID, in.TelegramMessageID, in.Status,
		in.ErrorCode, nullableStr(in.ErrorMessage),
	)
	if err != nil {
		return fmt.Errorf("insert send: %w", err)
	}
	return nil
}

// nullableStr — для error_message: пусто → NULL, не пустая строка → значение.
// Чтобы в БД не было \"\" вместо NULL (фильтрация EXISTS error_message легче).
func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// UserForSend — данные юзера для рассылки: telegram_id для send + метаданные
// для рендера шаблона.
type UserForSend struct {
	ID         int64
	TelegramID int64
	Username   string
	FirstName  string
	IsBanned   bool
}

// GetUserForSend читает данные одного юзера. nil если не найден или забанен
// (sender просто пропускает таких — записывает в broadcast_sends со
// status='failed', error='user banned/missing').
func (r *BroadcastRepository) GetUserForSend(ctx context.Context, userID int64) (*UserForSend, error) {
	var u UserForSend
	err := r.db.QueryRow(ctx, `
		SELECT id, telegram_id, COALESCE(username, ''),
		       COALESCE(first_name, ''), is_banned
		FROM users WHERE id = $1
	`, userID).Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName, &u.IsBanned)
	if err != nil {
		return nil, fmt.Errorf("get user %d: %w", userID, err)
	}
	return &u, nil
}

// IsAdmin проверяет роль по telegram_id. Используется api/broadcast.go
// чтобы заблочить approve/cancel от не-админов (защита от inject через
// callback_data forge).
func (r *BroadcastRepository) IsAdmin(ctx context.Context, telegramID int64) (bool, int64, error) {
	var userID int64
	var role string
	err := r.db.QueryRow(ctx, `
		SELECT id, role FROM users WHERE telegram_id = $1 AND is_banned = FALSE
	`, telegramID).Scan(&userID, &role)
	if err != nil {
		return false, 0, fmt.Errorf("lookup admin %d: %w", telegramID, err)
	}
	return role == "admin", userID, nil
}

// ListPendingDrafts возвращает drafts в status='draft' или 'approved',
// ещё не отправленные. Stage 5 (/admin команда) будет показывать этот
// список.
func (r *BroadcastRepository) ListPendingDrafts(ctx context.Context) ([]DraftSummary, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, segment_key, title, recipient_count, status, created_at, sent_at
		FROM broadcast_drafts
		WHERE status IN ('draft','approved','sending')
		ORDER BY created_at DESC
		LIMIT 50
	`)
	if err != nil {
		return nil, fmt.Errorf("list pending drafts: %w", err)
	}
	defer rows.Close()

	var out []DraftSummary
	for rows.Next() {
		var d DraftSummary
		if err := rows.Scan(&d.ID, &d.SegmentKey, &d.Title, &d.RecipientCount,
			&d.Status, &d.CreatedAt, &d.SentAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
