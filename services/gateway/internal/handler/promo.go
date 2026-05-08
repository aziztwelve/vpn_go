// Package handler — promo.go: публичный redeem-flow для одноразовых
// промо-ссылок.
//
// URL: GET /promo/p/{token} (без JWT, без rate-limit от bot-команды
// admin-only).
//
// Алгоритм:
//   1. PromoClient.Redeem(token) — резолвит token → user_id+plan_id+max_devices.
//      Если уже есть payment_id (двойной клик) → редиректим на тот же
//      invoice_link (idempotent).
//   2. PaymentClient.CreateInvoice(user_id, plan_id, max_devices, "platega")
//      — создаёт pending payment + invoice.
//   3. PromoClient.AttachPayment(token, payment_id) — связываем для
//      последующего MarkUsed-webhook'а.
//   4. 302 Redirect → invoice_link (Platega payment URL).
//
// Edge-cases (HTML-страницы вместо 302):
//   - token не найден / expired — «ссылка устарела»
//   - already_used — «промо уже использован, спасибо за оплату»
//   - сбой CreateInvoice — «не удалось создать счёт, попробуйте позже»
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vpn/gateway/internal/client"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type PromoHandler struct {
	promoClient   *client.PromoClient
	paymentClient *client.PaymentClient
	logger        *zap.Logger
}

func NewPromoHandler(
	promoClient *client.PromoClient,
	paymentClient *client.PaymentClient,
	logger *zap.Logger,
) *PromoHandler {
	return &PromoHandler{
		promoClient:   promoClient,
		paymentClient: paymentClient,
		logger:        logger,
	}
}

// Redeem — GET /promo/p/{token}.
// Публичный, без JWT. Реализует алгоритм из doc-комментария пакета.
func (h *PromoHandler) Redeem(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" || len(token) < 16 {
		writePromoError(w, http.StatusBadRequest,
			"Неверный формат ссылки",
			"Параметры промо-кода повреждены. Проверьте ссылку.")
		return
	}

	// Изолированный 5s-context — чтобы зависший gRPC не залочил handler.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	redeemResp, err := h.promoClient.Redeem(ctx, token)
	if err != nil {
		h.handleRedeemError(w, token, err)
		return
	}

	// Если промо уже использован (paid) — отдельная страница.
	if redeemResp.AlreadyUsed {
		writePromoError(w, http.StatusGone,
			"Промо уже использован",
			"Спасибо за оплату! Подписка активна. Откройте бота, чтобы подключить VPN.")
		return
	}

	// Idempotent retry: если payment_id уже привязан к токену (юзер
	// кликнул дважды до оплаты), переиспользуем существующий invoice.
	if redeemResp.ExistingPaymentId > 0 {
		invoiceLink, err := h.fetchExistingInvoiceLink(ctx, redeemResp.ExistingPaymentId)
		if err == nil && invoiceLink != "" {
			h.logger.Info("promo: redirecting to existing invoice",
				zap.String("token_prefix", safePrefix(token)),
				zap.Int64("payment_id", redeemResp.ExistingPaymentId))
			http.Redirect(w, r, invoiceLink, http.StatusFound)
			return
		}
		// Если не смогли подтянуть — fall through на CreateInvoice.
		// На стороне payment-service создастся новый pending — это OK,
		// у юзера в итоге будет свежая ссылка.
		h.logger.Warn("promo: failed to fetch existing invoice, creating new",
			zap.Int64("payment_id", redeemResp.ExistingPaymentId),
			zap.Error(err))
	}

	// Создаём invoice через Platega (единственный включённый провайдер).
	invResp, err := h.paymentClient.CreateInvoice(
		ctx,
		redeemResp.UserId,
		redeemResp.PlanId,
		redeemResp.MaxDevices,
		"platega",
	)
	if err != nil {
		h.logger.Error("promo: CreateInvoice failed",
			zap.String("token_prefix", safePrefix(token)),
			zap.Int64("user_id", redeemResp.UserId),
			zap.Int32("plan_id", redeemResp.PlanId),
			zap.Error(err))
		writePromoError(w, http.StatusServiceUnavailable,
			"Не удалось создать счёт",
			"Попробуйте позже или напишите в поддержку: @maydavpn_support")
		return
	}

	// Привязываем payment к промо. Не критично если упадёт — webhook
	// MarkUsed просто не сработает, но юзер получит подписку. Логируем
	// как warning, не error.
	if err := h.promoClient.AttachPayment(ctx, token, invResp.PaymentId); err != nil {
		h.logger.Warn("promo: AttachPayment failed (non-fatal)",
			zap.String("token_prefix", safePrefix(token)),
			zap.Int64("payment_id", invResp.PaymentId),
			zap.Error(err))
	}

	h.logger.Info("promo: redirecting to fresh invoice",
		zap.String("token_prefix", safePrefix(token)),
		zap.Int64("user_id", redeemResp.UserId),
		zap.Int64("payment_id", invResp.PaymentId))

	http.Redirect(w, r, invResp.InvoiceLink, http.StatusFound)
}

// handleRedeemError маппит gRPC-ошибки на красивые HTML-страницы.
func (h *PromoHandler) handleRedeemError(w http.ResponseWriter, token string, err error) {
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.NotFound:
			writePromoError(w, http.StatusNotFound,
				"Ссылка не найдена",
				"Возможно, ссылка устарела или в неё вкралась опечатка. Напишите в поддержку: @maydavpn_support")
			return
		case codes.FailedPrecondition:
			writePromoError(w, http.StatusGone,
				"Срок действия истёк",
				"Эта промо-ссылка больше не активна. Напишите в поддержку, мы выпустим новую: @maydavpn_support")
			return
		}
	}
	h.logger.Error("promo: Redeem failed",
		zap.String("token_prefix", safePrefix(token)),
		zap.Error(err))
	writePromoError(w, http.StatusInternalServerError,
		"Что-то пошло не так",
		"Попробуйте обновить страницу через минуту. Если не помогло — поддержка: @maydavpn_support")
}

// fetchExistingInvoiceLink возвращает invoice_link для существующего payment.
// payment-service не отдаёт invoice_link напрямую через GetPayment (только
// статус и meta), потому пока возвращаем пустую строку — fallback на
// создание нового invoice. TODO: добавить GetInvoiceLink в payment-proto.
func (h *PromoHandler) fetchExistingInvoiceLink(_ context.Context, _ int64) (string, error) {
	// Placeholder для будущей оптимизации: на втором клике до оплаты
	// мы создадим ВТОРОЙ invoice. Это безопасно (Platega allows multiple
	// pending invoices for same user), но создаёт мусор в `payments`.
	// Когда добавим payment.GetInvoiceLink → здесь подтянем и вернём.
	return "", errors.New("not implemented")
}

// writePromoError рендерит минималистичную HTML-страницу.
// Без JS, без CSS-фреймворков — нужно работать в Telegram WebView,
// браузерах российских банков и в IE-обёртках провайдеров.
func writePromoError(w http.ResponseWriter, statusCode int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="ru"><head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>%s — MaydaVPN</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
         background:#0f172a; color:#e2e8f0; margin:0; padding:24px;
         display:flex; align-items:center; justify-content:center; min-height:100vh; }
  .card { max-width:420px; width:100%%; background:#1e293b; border:1px solid #334155;
          border-radius:16px; padding:32px 24px; text-align:center; }
  h1 { font-size:20px; margin:0 0 12px; color:#f1f5f9; }
  p { font-size:15px; line-height:1.5; margin:0 0 20px; color:#cbd5e1; }
  a.btn { display:inline-block; padding:10px 20px; background:#3b82f6; color:#fff;
          border-radius:8px; text-decoration:none; font-weight:600; }
  a.btn:hover { background:#2563eb; }
</style>
</head><body><div class="card">
<h1>%s</h1>
<p>%s</p>
<a class="btn" href="https://t.me/maydavpn_support">Написать в поддержку</a>
</div></body></html>`, escapeHTML(title), escapeHTML(title), escapeHTML(body))
}

// escapeHTML — минимальный escape для server-rendered HTML.
func escapeHTML(s string) string {
	r := []rune(s)
	out := make([]rune, 0, len(r)+8)
	for _, c := range r {
		switch c {
		case '<':
			out = append(out, []rune("&lt;")...)
		case '>':
			out = append(out, []rune("&gt;")...)
		case '&':
			out = append(out, []rune("&amp;")...)
		case '"':
			out = append(out, []rune("&quot;")...)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}
