package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vpn/gateway/internal/client"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// CreateInvoice — POST /api/v1/payments?provider=telegram_stars (защищённая JWT).
// Возвращает invoice_link; Mini App открывает через WebApp.openInvoice() или в браузере.
func (h *PaymentHandler) CreateInvoice(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}

	// Получаем провайдер из query parameter.
	// Default = "platega" — на текущий момент включён только этот провайдер
	// (см. docs/services/platega-integration.md). Остальные (telegram_stars,
	// wata, yoomoney) можно включить через *_ENABLED env-флаги — фронт явно
	// передаёт ?provider=... когда селектор расширяется.
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		provider = "platega"
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

	resp, err := h.paymentClient.CreateInvoice(r.Context(), userID, req.PlanID, req.MaxDevices, provider)
	if err != nil {
		h.logger.Error("CreateInvoice failed",
			zap.Int64("user_id", userID),
			zap.String("provider", provider),
			zap.Error(err))
		http.Error(w, "failed to create invoice", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"payment_id":   resp.PaymentId,
		"invoice_link": resp.InvoiceLink,
		"amount_stars": resp.AmountStars,
		"provider":     provider,
	})
}

// GetPayment — GET /api/v1/payments/{id} (защищённая JWT).
// Возвращает один платёж по ID. Используется фронтом на /payment/pending для
// поллинга статуса (вместо тяжёлого ListUserPayments(100)).
//
// Авторизация: payment.user_id обязан совпадать с user_id из JWT —
// иначе 404 (не раскрываем существование чужого платежа).
func (h *PaymentHandler) GetPayment(w http.ResponseWriter, r *http.Request) {
	userID, ok := userIDFromRequest(w, r)
	if !ok {
		return
	}

	idStr := chi.URLParam(r, "id")
	paymentID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || paymentID <= 0 {
		http.Error(w, "invalid payment id", http.StatusBadRequest)
		return
	}

	resp, err := h.paymentClient.GetPayment(r.Context(), paymentID)
	if err != nil {
		if st, stOK := status.FromError(err); stOK && st.Code() == codes.NotFound {
			http.Error(w, "payment not found", http.StatusNotFound)
			return
		}
		h.logger.Error("GetPayment failed",
			zap.Int64("payment_id", paymentID),
			zap.Int64("user_id", userID),
			zap.Error(err))
		http.Error(w, "failed to get payment", http.StatusInternalServerError)
		return
	}

	p := resp.GetPayment()
	if p == nil || p.GetUserId() != userID {
		// Чужой платёж или пустой ответ — отдаём 404, не раскрывая факт
		// существования платежа.
		http.Error(w, "payment not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
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

// HandleWebhook — POST /api/v1/payments/webhook/{provider} (ПУБЛИЧНАЯ!).
// Универсальный обработчик webhook для всех провайдеров.
// Провайдер определяется из URL path: /webhook/telegram, /webhook/yoomoney, /webhook/yookassa
func (h *PaymentHandler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	// Извлекаем провайдера из URL (последний сегмент)
	// Например: /api/v1/payments/webhook/yoomoney → provider = yoomoney
	pathParts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
	if len(pathParts) == 0 {
		http.Error(w, "provider not specified", http.StatusBadRequest)
		return
	}
	provider := pathParts[len(pathParts)-1]

	// Читаем body
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Получаем подпись из headers (разные провайдеры используют разные headers)
	var signature string
	switch provider {
	case "telegram_stars":
		signature = r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	case "yoomoney":
		// YooMoney передаёт подпись в теле запроса (sha1_hash)
		signature = ""
	case "yookassa":
		// ЮKassa использует HTTP Basic Auth
		signature = r.Header.Get("Authorization")
	case "wata":
		// WATA: RSA-SHA512 base64 в X-Signature
		signature = r.Header.Get("X-Signature")
	case "platega":
		// Platega: подписи нет, аутентификация через сравнение двух хедеров
		// X-MerchantId / X-Secret. Склеиваем их через ":" и передаём в
		// payment-service единым полем signature — provider распарсит
		// strings.Cut(":", 2) и сверит через subtle.ConstantTimeCompare.
		signature = r.Header.Get("X-MerchantId") + ":" + r.Header.Get("X-Secret")
	}

	// Вызываем payment-service
	resp, err := h.paymentClient.HandleWebhook(r.Context(), provider, body, signature)
	if err != nil {
		h.logger.Error("HandleWebhook failed",
			zap.String("provider", provider),
			zap.Error(err),
			zap.Int("body_len", len(body)),
		)
		// 5xx → провайдер ретраит
		http.Error(w, "webhook handler failed", http.StatusInternalServerError)
		return
	}

	h.logger.Info("webhook processed",
		zap.String("provider", provider),
		zap.String("status", resp.Status),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":     true,
		"status": resp.Status,
	})
}
