// admin_campaigns.go — REST-handlers для управления маркетинговыми воронками.
//
// Все ручки — под middleware.RequireAdmin (см. app.go). Юзер с role='admin'
// в JWT может:
//   GET    /api/v1/admin/campaigns                    — список с базовой статистикой
//   POST   /api/v1/admin/campaigns                    — создать
//   GET    /api/v1/admin/campaigns/{id}               — получить + воронка за период
//   PATCH  /api/v1/admin/campaigns/{id}               — обновить name/notes/partner/%
//   POST   /api/v1/admin/campaigns/{id}/archive       — архивация (soft-delete)
//   GET    /api/v1/admin/campaigns/{id}/stats         — только воронка за период
//
// Протокол ошибок:
//   - 400 на невалидный ввод (slug, процент, body)
//   - 403 если middleware не пустила (не-admin) — отрабатывает RequireAdmin
//   - 404 если кампании нет
//   - 409 на конфликт slug'а
//   - 500 на всё остальное (логируется с контекстом)
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vpn/gateway/internal/client"
	campaignpb "github.com/vpn/shared/pkg/proto/campaign/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AdminCampaignsHandler struct {
	campaign *client.CampaignClient
	logger   *zap.Logger
}

func NewAdminCampaignsHandler(campaign *client.CampaignClient, logger *zap.Logger) *AdminCampaignsHandler {
	return &AdminCampaignsHandler{campaign: campaign, logger: logger}
}

// ─── List ──────────────────────────────────────────────────────────

// GET /api/v1/admin/campaigns?include_archived=1&limit=100&offset=0
func (h *AdminCampaignsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	includeArchived := q.Get("include_archived") == "1" || q.Get("include_archived") == "true"
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	resp, err := h.campaign.ListCampaigns(r.Context(), includeArchived, int32(limit), int32(offset))
	if err != nil {
		h.logger.Error("ListCampaigns failed", zap.Error(err))
		mapCampaignGRPCErr(w, err)
		return
	}
	items := make([]map[string]any, 0, len(resp.Campaigns))
	for _, c := range resp.Campaigns {
		items = append(items, campaignWithStatsToJSON(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"campaigns": items,
		"total":     resp.Total,
	})
}

// ─── Create ────────────────────────────────────────────────────────

type createCampaignReq struct {
	Slug              string `json:"slug"`
	Name              string `json:"name"`
	Notes             string `json:"notes"`
	PartnerUserID     int64  `json:"partner_user_id"`
	PayoutPercent     int32  `json:"payout_percent"`
	// 0 = без override (дефолт 3 дня); 3/7/15/30/60/90 = override (см. task 19).
	TrialDurationDays int32  `json:"trial_duration_days"`
}

// POST /api/v1/admin/campaigns
func (h *AdminCampaignsHandler) Create(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}
	var req createCampaignReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	c, err := h.campaign.CreateCampaign(r.Context(), &campaignpb.CreateCampaignRequest{
		Slug:              req.Slug,
		Name:              req.Name,
		Notes:             req.Notes,
		PartnerUserId:     req.PartnerUserID,
		PayoutPercent:     req.PayoutPercent,
		CreatedByUserId:   uid,
		TrialDurationDays: req.TrialDurationDays,
	})
	if err != nil {
		h.logger.Error("CreateCampaign failed",
			zap.Int64("admin_id", uid),
			zap.String("slug", req.Slug),
			zap.Error(err),
		)
		mapCampaignGRPCErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"campaign": campaignToJSON(c)})
}

// ─── Get (+ stats за период) ───────────────────────────────────────

// GET /api/v1/admin/campaigns/{id}?from=...&to=...
func (h *AdminCampaignsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	q := r.URL.Query()
	resp, err := h.campaign.GetCampaign(r.Context(), id, q.Get("from"), q.Get("to"))
	if err != nil {
		h.logger.Error("GetCampaign failed", zap.Int64("id", id), zap.Error(err))
		mapCampaignGRPCErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, campaignWithStatsToJSON(resp))
}

// ─── Update (partial) ──────────────────────────────────────────────

type updateCampaignReq struct {
	Name              *string `json:"name"`
	Notes             *string `json:"notes"`
	PartnerUserID     *int64  `json:"partner_user_id"`     // -1 = clear
	PayoutPercent     *int32  `json:"payout_percent"`      // -1 = clear
	// nil = не менять; -1 = clear (вернуть к дефолту 3 дня); 3/7/15/30/60/90 = override.
	TrialDurationDays *int32  `json:"trial_duration_days"`
}

// PATCH /api/v1/admin/campaigns/{id}
func (h *AdminCampaignsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	var req updateCampaignReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	// proto использует "" / 0 / -1 → собираем запрос вручную.
	protoReq := &campaignpb.UpdateCampaignRequest{Id: id}
	if req.Name != nil {
		protoReq.Name = *req.Name
	}
	if req.Notes != nil {
		protoReq.Notes = *req.Notes
	}
	if req.PartnerUserID != nil {
		protoReq.PartnerUserId = *req.PartnerUserID
	}
	if req.PayoutPercent != nil {
		protoReq.PayoutPercent = *req.PayoutPercent
	}
	if req.TrialDurationDays != nil {
		protoReq.TrialDurationDays = *req.TrialDurationDays
	}

	c, err := h.campaign.UpdateCampaign(r.Context(), protoReq)
	if err != nil {
		h.logger.Error("UpdateCampaign failed", zap.Int64("id", id), zap.Error(err))
		mapCampaignGRPCErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaign": campaignToJSON(c)})
}

// ─── Archive ───────────────────────────────────────────────────────

// POST /api/v1/admin/campaigns/{id}/archive
func (h *AdminCampaignsHandler) Archive(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	c, err := h.campaign.ArchiveCampaign(r.Context(), id)
	if err != nil {
		h.logger.Error("ArchiveCampaign failed", zap.Int64("id", id), zap.Error(err))
		mapCampaignGRPCErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaign": campaignToJSON(c)})
}

// ─── Stats ─────────────────────────────────────────────────────────

// GET /api/v1/admin/campaigns/{id}/stats?from=...&to=...
func (h *AdminCampaignsHandler) Stats(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	q := r.URL.Query()
	st, err := h.campaign.GetCampaignStats(r.Context(), id, q.Get("from"), q.Get("to"))
	if err != nil {
		h.logger.Error("GetCampaignStats failed", zap.Int64("id", id), zap.Error(err))
		mapCampaignGRPCErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statsToJSON(st))
}

// ─── helpers ───────────────────────────────────────────────────────

func parseIDParam(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	raw := chi.URLParam(r, name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func campaignToJSON(c *campaignpb.Campaign) map[string]any {
	out := map[string]any{
		"id":                  c.Id,
		"slug":                c.Slug,
		"name":                c.Name,
		"notes":               c.Notes,
		"partner_user_id":     c.PartnerUserId,
		"payout_percent":      c.PayoutPercent,
		"is_active":           c.IsActive,
		"created_by":          c.CreatedBy,
		"created_at":          c.CreatedAt,
		"archived_at":         c.ArchivedAt,
		"deep_link":           c.DeepLink,
		"trial_duration_days": nil,
	}
	if c.TrialDurationDays > 0 {
		out["trial_duration_days"] = c.TrialDurationDays
	}
	return out
}

func statsToJSON(s *campaignpb.CampaignStats) map[string]any {
	return map[string]any{
		"campaign_id":         s.CampaignId,
		"starts":              s.Starts,
		"opened_app":          s.OpenedApp,
		"trial_activated":     s.TrialActivated,
		"paid_users":          s.PaidUsers,
		"revenue_rub":         s.RevenueRub,
		"partner_payouts_rub": s.PartnerPayoutsRub,
		"from":                s.From,
		"to":                  s.To,
	}
}

func campaignWithStatsToJSON(cws *campaignpb.CampaignWithStats) map[string]any {
	return map[string]any{
		"campaign": campaignToJSON(cws.Campaign),
		"stats":    statsToJSON(cws.Stats),
	}
}

// mapCampaignGRPCErr — маппинг gRPC status → HTTP-код.
func mapCampaignGRPCErr(w http.ResponseWriter, err error) {
	st, ok := status.FromError(err)
	if !ok {
		// Не gRPC-ошибка — обычная внутренняя.
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	msg := st.Message()
	switch st.Code() {
	case codes.NotFound:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "message": msg})
	case codes.AlreadyExists:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict", "message": msg})
	case codes.InvalidArgument:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "message": msg})
	case codes.PermissionDenied:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "message": msg})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal", "message": msg})
	}
}

// errIsGRPCNotFound — sentinel хелпер (на случай миграции кода).
// Не используется, оставлен для симметрии с чеками в других handler'ах.
var _ = errors.New
