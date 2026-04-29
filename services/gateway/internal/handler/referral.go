// Package handler — REST handlers Gateway. Этот файл — реферальная программа:
//   GET  /api/v1/referral/link        — получить/создать реф-ссылку юзера
//   GET  /api/v1/referral/stats       — статистика (приглашённые, дни, баланс)
//   POST /api/v1/referral/withdrawal  — создать заявку на вывод (только partner)
//   GET  /api/v1/referral/withdrawals — список заявок текущего юзера
//
// Все ручки требуют JWT (user_id берётся из context middleware'а).
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/vpn/gateway/internal/client"
	"go.uber.org/zap"
)

type ReferralHandler struct {
	referral *client.ReferralClient
	logger   *zap.Logger
}

func NewReferralHandler(ref *client.ReferralClient, logger *zap.Logger) *ReferralHandler {
	return &ReferralHandler{referral: ref, logger: logger}
}

// GET /api/v1/referral/link
func (h *ReferralHandler) GetLink(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}
	resp, err := h.referral.GetOrCreateLink(r.Context(), uid)
	if err != nil {
		h.logger.Error("referral.GetOrCreateLink failed", zap.Int64("user_id", uid), zap.Error(err))
		http.Error(w, "Failed to get referral link", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url":         resp.Url,
		"token":       resp.Token,
		"click_count": resp.ClickCount,
	})
}

// GET /api/v1/referral/stats
func (h *ReferralHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}
	resp, err := h.referral.GetStats(r.Context(), uid)
	if err != nil {
		h.logger.Error("referral.GetStats failed", zap.Int64("user_id", uid), zap.Error(err))
		http.Error(w, "Failed to get referral stats", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"invited_count":            resp.InvitedCount,
		"purchased_count":          resp.PurchasedCount,
		"rewarded_days_total":      resp.RewardedDaysTotal,
		"earned_balance_rub_total": resp.EarnedBalanceRubTotal,
		"current_balance_rub":      resp.CurrentBalanceRub,
		"pending_count":            resp.PendingCount,
	})
}

// POST /api/v1/referral/withdrawal
type CreateWithdrawalRequest struct {
	AmountRub      string            `json:"amount_rub"`
	PaymentMethod  string            `json:"payment_method"`
	PaymentDetails map[string]string `json:"payment_details"`
}

func (h *ReferralHandler) CreateWithdrawal(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}
	var req CreateWithdrawalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AmountRub == "" || req.PaymentMethod == "" {
		http.Error(w, "amount_rub and payment_method are required", http.StatusBadRequest)
		return
	}
	resp, err := h.referral.CreateWithdrawal(r.Context(), uid, req.AmountRub, req.PaymentMethod, req.PaymentDetails)
	if err != nil {
		h.logger.Error("referral.CreateWithdrawal failed", zap.Int64("user_id", uid), zap.Error(err))
		http.Error(w, "Failed to create withdrawal", http.StatusInternalServerError)
		return
	}
	if resp.Error != "" {
		// Бизнес-ошибки (insufficient_balance / not_partner / amount_too_small)
		// — отдаём 400 с кодом, чтобы фронт мог нормально показать.
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": resp.Error,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"request": map[string]any{
			"id":              resp.Request.Id,
			"amount_rub":      resp.Request.AmountRub,
			"payment_method":  resp.Request.PaymentMethod,
			"payment_details": resp.Request.PaymentDetails,
			"status":          resp.Request.Status,
			"created_at":      resp.Request.CreatedAt,
		},
	})
}

// GET /api/v1/referral/withdrawals?status=pending&limit=50&offset=0
func (h *ReferralHandler) ListWithdrawals(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	resp, err := h.referral.ListWithdrawals(r.Context(), uid, status, int32(limit), int32(offset))
	if err != nil {
		h.logger.Error("referral.ListWithdrawals failed", zap.Int64("user_id", uid), zap.Error(err))
		http.Error(w, "Failed to list withdrawals", http.StatusInternalServerError)
		return
	}
	items := make([]map[string]any, 0, len(resp.Requests))
	for _, wr := range resp.Requests {
		items = append(items, map[string]any{
			"id":              wr.Id,
			"amount_rub":      wr.AmountRub,
			"payment_method":  wr.PaymentMethod,
			"payment_details": wr.PaymentDetails,
			"status":          wr.Status,
			"admin_comment":   wr.AdminComment,
			"created_at":      wr.CreatedAt,
			"processed_at":    wr.ProcessedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requests": items,
		"total":    resp.Total,
	})
}

// writeJSON — короткий хелпер. (В существующих handler'ах используется
// прямой json.NewEncoder — оставляем тот же стиль.)
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
