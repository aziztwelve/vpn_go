package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/vpn/gateway/internal/client"
	"go.uber.org/zap"
)

type PaymentHandler struct {
	paymentClient *client.PaymentClient
	logger        *zap.Logger
	// WebhookSecret — то же значение что `secret_token` в setWebhook.
	// Telegram шлёт его в header X-Telegram-Bot-Api-Secret-Token.
	// Пустая строка = не проверяем (только для dev).
	webhookSecret string
}

func NewPaymentHandler(paymentClient *client.PaymentClient, webhookSecret string, logger *zap.Logger) *PaymentHandler {
	return &PaymentHandler{
		paymentClient: paymentClient,
		logger:        logger,
		webhookSecret: webhookSecret,
	}
}

// CreateInvoiceRequest — body для POST /api/v1/payments.
type CreateInvoiceRequest struct {
	PlanID     int32 `json:"plan_id"`
	MaxDevices int32 `json:"max_devices"`
}

// CreateInvoice — POST /api/v1/payments (защищённая JWT).
// Возвращает t.me/$... invoice_link; Mini App открывает через WebApp.openInvoice().
func (h *PaymentHandler) CreateInvoice(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}

	var req CreateInvoiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.PlanID == 0 || req.MaxDevices == 0 {
		http.Error(w, "plan_id and max_devices are required", http.StatusBadRequest)
		return
	}

	resp, err := h.paymentClient.CreateInvoice(r.Context(), userID, req.PlanID, req.MaxDevices)
	if err != nil {
		h.logger.Error("CreateInvoice failed",
			zap.Int64("user_id", userID), zap.Error(err))
		http.Error(w, "failed to create invoice", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"payment_id":   resp.PaymentId,
		"invoice_link": resp.InvoiceLink,
		"amount_stars": resp.AmountStars,
	})
}

// ListPayments — GET /api/v1/payments?limit=50&offset=0 (защищённая JWT).
func (h *PaymentHandler) ListPayments(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}

	var limit, offset int32
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			limit = int32(n)
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			offset = int32(n)
		}
	}

	resp, err := h.paymentClient.ListUserPayments(r.Context(), userID, limit, offset)
	if err != nil {
		h.logger.Error("ListUserPayments failed", zap.Error(err))
		http.Error(w, "failed to list payments", http.StatusInternalServerError)
		return
	}

	// Маппим proto → snake_case явно, чтобы фронт не зависел от тегов protobuf.
	payments := make([]map[string]interface{}, 0, len(resp.Payments))
	for _, p := range resp.Payments {
		payments = append(payments, map[string]interface{}{
			"id":           p.Id,
			"user_id":      p.UserId,
			"plan_id":      p.PlanId,
			"max_devices":  p.MaxDevices,
			"amount_stars": p.AmountStars,
			"status":       p.Status,
			"external_id":  p.ExternalId,
			"provider":     p.Provider,
			"created_at":   p.CreatedAt,
			"paid_at":      p.PaidAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"payments": payments,
	})
}

// TelegramWebhook — POST /api/v1/telegram/webhook (ПУБЛИЧНАЯ!).
// Защищена shared-секретом через header X-Telegram-Bot-Api-Secret-Token.
// Telegram шлёт сюда pre_checkout_query / successful_payment / refunded_payment.
//
// ВАЖНО для идемпотентности: отвечаем 200 на любой "известный" update
// (даже если это duplicate/ignored), чтобы Telegram не ретраил 30 минут.
// 5xx возвращаем только при реальных серверных сбоях.
func (h *PaymentHandler) TelegramWebhook(w http.ResponseWriter, r *http.Request) {
	// Валидация shared secret (если настроен).
	if h.webhookSecret != "" {
		got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		if got != h.webhookSecret {
			h.logger.Warn("telegram webhook: invalid secret token",
				zap.String("remote", r.RemoteAddr))
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB — у Telegram updates всегда меньше
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	resp, err := h.paymentClient.HandleTelegramUpdate(r.Context(), body)
	if err != nil {
		h.logger.Error("HandleTelegramUpdate failed",
			zap.Error(err),
			zap.Int("body_len", len(body)),
		)
		// 5xx → Telegram ретраит. Используем только для реальных серверных ошибок.
		http.Error(w, "webhook handler failed", http.StatusInternalServerError)
		return
	}

	h.logger.Info("telegram webhook processed",
		zap.String("action", resp.Action),
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":     true,
		"action": resp.Action,
	})
}
