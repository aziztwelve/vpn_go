// admin_broadcasts.go — REST-handlers для управления retention-рассылками.
//
// Все ручки — под middleware.RequireAdmin (см. app.go). admin_user_id
// берётся из JWT (положен auth-middleware'ом). auth-service сам проверяет
// users.role='admin' для этого user_id и возвращает PermissionDenied
// если что-то не сходится.
//
// Маршруты (под /api/v1/admin/broadcasts):
//   GET     /                                  — список (фильтры status/segment)
//   GET     /{id}                              — детали + статы доставки
//   PATCH   /{id}                              — title/body/buttons (только status='draft')
//   POST    /{id}/approve                      — запустить рассылку
//   POST    /{id}/cancel                       — отменить draft
//
// Протокол ошибок: NotFound→404, InvalidArgument→400, PermissionDenied→403,
// FailedPrecondition→409, остальное→500. Тело ошибки —
// {"error":"<short>","message":"<from gRPC>"}.
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/vpn/gateway/internal/client"
	broadcastpb "github.com/vpn/shared/pkg/proto/broadcast/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AdminBroadcastsHandler struct {
	broadcast *client.BroadcastClient
	logger    *zap.Logger
}

func NewAdminBroadcastsHandler(broadcast *client.BroadcastClient, logger *zap.Logger) *AdminBroadcastsHandler {
	return &AdminBroadcastsHandler{broadcast: broadcast, logger: logger}
}

// ─── List ──────────────────────────────────────────────────────────

// GET /api/v1/admin/broadcasts?status=draft&segment=trial_never_connected&limit=50&offset=0
func (h *AdminBroadcastsHandler) List(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	resp, err := h.broadcast.ListBroadcasts(r.Context(), uid,
		q.Get("status"), q.Get("segment"),
		int32(limit), int32(offset))
	if err != nil {
		h.logger.Error("ListBroadcasts failed",
			zap.Int64("admin_id", uid), zap.Error(err))
		mapBroadcastGRPCErr(w, err)
		return
	}

	items := make([]map[string]any, 0, len(resp.Items))
	for _, d := range resp.Items {
		items = append(items, draftSummaryJSON(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"broadcasts": items,
		"total":      resp.Total,
	})
}

// ─── Get ───────────────────────────────────────────────────────────

// GET /api/v1/admin/broadcasts/{id}
func (h *AdminBroadcastsHandler) Get(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	d, err := h.broadcast.GetBroadcastDetails(r.Context(), id, uid)
	if err != nil {
		mapBroadcastGRPCErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, broadcastDetailsJSON(d))
}

// ─── Update ────────────────────────────────────────────────────────

type updateBroadcastReq struct {
	Title   string                 `json:"title,omitempty"`         // "" = не менять
	Body    string                 `json:"body_template,omitempty"` // "" = не менять
	Buttons []updateBroadcastButton `json:"buttons,omitempty"`      // nil = не менять
}

type updateBroadcastButton struct {
	Text string `json:"text"`
	Type string `json:"type"`           // 'web_app' | 'url' | 'callback_data'
	URL  string `json:"url,omitempty"`
	Data string `json:"data,omitempty"`
}

// PATCH /api/v1/admin/broadcasts/{id}
func (h *AdminBroadcastsHandler) Update(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	var req updateBroadcastReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var protoButtons []*broadcastpb.Button
	if req.Buttons != nil {
		protoButtons = make([]*broadcastpb.Button, 0, len(req.Buttons))
		for _, b := range req.Buttons {
			protoButtons = append(protoButtons, &broadcastpb.Button{
				Text: b.Text, Type: b.Type, Url: b.URL, Data: b.Data,
			})
		}
	}

	resp, err := h.broadcast.UpdateBroadcast(r.Context(), id, uid, req.Title, req.Body, protoButtons)
	if err != nil {
		mapBroadcastGRPCErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, draftSummaryJSON(resp))
}

// ─── Approve ───────────────────────────────────────────────────────

// POST /api/v1/admin/broadcasts/{id}/approve
func (h *AdminBroadcastsHandler) Approve(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	resp, err := h.broadcast.ApproveBroadcast(r.Context(), id, uid)
	if err != nil {
		mapBroadcastGRPCErr(w, err)
		return
	}
	h.logger.Info("admin approved broadcast",
		zap.Int64("admin_id", uid),
		zap.Int64("draft_id", id),
		zap.Int32("recipients", resp.RecipientCount),
	)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"draft_id":        resp.DraftId,
		"status":          resp.Status,
		"recipient_count": resp.RecipientCount,
	})
}

// ─── Cancel ────────────────────────────────────────────────────────

// POST /api/v1/admin/broadcasts/{id}/cancel
func (h *AdminBroadcastsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	resp, err := h.broadcast.CancelBroadcast(r.Context(), id, uid)
	if err != nil {
		mapBroadcastGRPCErr(w, err)
		return
	}
	h.logger.Info("admin cancelled broadcast",
		zap.Int64("admin_id", uid), zap.Int64("draft_id", id))
	writeJSON(w, http.StatusOK, map[string]any{
		"draft_id": resp.DraftId,
		"status":   resp.Status,
	})
}

// ─── helpers ───────────────────────────────────────────────────────

func draftSummaryJSON(d *broadcastpb.DraftSummary) map[string]any {
	out := map[string]any{
		"id":              d.Id,
		"segment_key":     d.SegmentKey,
		"title":           d.Title,
		"recipient_count": d.RecipientCount,
		"status":          d.Status,
		"created_at_unix": d.CreatedAtUnix,
	}
	if d.SentAtUnix > 0 {
		out["sent_at_unix"] = d.SentAtUnix
	}
	return out
}

func broadcastDetailsJSON(d *broadcastpb.BroadcastDetails) map[string]any {
	buttons := make([]map[string]any, 0, len(d.Buttons))
	for _, b := range d.Buttons {
		buttons = append(buttons, map[string]any{
			"text": b.Text, "type": b.Type, "url": b.Url, "data": b.Data,
		})
	}
	out := map[string]any{
		"id":              d.Id,
		"segment_key":     d.SegmentKey,
		"title":           d.Title,
		"body_template":   d.BodyTemplate,
		"buttons":         buttons,
		"recipient_count": d.RecipientCount,
		"status":          d.Status,
		"created_at_unix": d.CreatedAtUnix,
	}
	if d.ApprovedAtUnix > 0 {
		out["approved_at_unix"] = d.ApprovedAtUnix
		out["approved_by_user_id"] = d.ApprovedByUserId
	}
	if d.SentAtUnix > 0 {
		out["sent_at_unix"] = d.SentAtUnix
	}
	if d.Stats != nil {
		out["stats"] = map[string]any{
			"sent":    d.Stats.Sent,
			"blocked": d.Stats.Blocked,
			"failed":  d.Stats.Failed,
			"opened":  d.Stats.Opened,
			"clicked": d.Stats.Clicked,
		}
	}
	return out
}

// mapBroadcastGRPCErr — gRPC status → HTTP-код.
func mapBroadcastGRPCErr(w http.ResponseWriter, err error) {
	st, ok := status.FromError(err)
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	msg := st.Message()
	switch st.Code() {
	case codes.NotFound:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "message": msg})
	case codes.InvalidArgument:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "message": msg})
	case codes.PermissionDenied:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "message": msg})
	case codes.FailedPrecondition:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict", "message": msg})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal", "message": msg})
	}
}

