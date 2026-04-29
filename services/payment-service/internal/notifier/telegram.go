// Package notifier — отправка push-уведомлений юзерам через Telegram Bot API.
//
// Используется payment-service'ом после успешной обработки webhook'а оплаты:
// юзер мог закрыть Mini App, поэтому уведомление в чат с ботом — единственный
// надёжный канал сказать "✅ оплата прошла".
//
// Намеренно не оборачиваем в provider.PaymentProvider интерфейс — это не
// провайдер платежа, а исходящий side-effect. Работает независимо от того,
// зарегистрирован ли Telegram Stars как провайдер.
package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

const (
	apiURLFormat = "https://api.telegram.org/bot%s/%s"
	httpTimeout  = 10 * time.Second
)

// Telegram — отправщик сообщений в чат с ботом.
type Telegram struct {
	botToken string
	client   *http.Client
	logger   *zap.Logger
}

// New создаёт нотификатор. Если botToken пустой — возвращает nil
// (вызывающий должен это проверить и пропускать вызовы).
func New(botToken string, logger *zap.Logger) *Telegram {
	if botToken == "" {
		return nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Telegram{
		botToken: botToken,
		client:   &http.Client{Timeout: httpTimeout},
		logger:   logger,
	}
}

// NotifyPaid отправляет юзеру сообщение об успешной оплате.
//
// Параметры:
//   - chatID  — Telegram user_id (для прямых сообщений совпадает с chat_id).
//   - planName — название тарифа ("3 месяца", "1 год", …).
//   - expiresAtRFC3339 — когда заканчивается подписка (ISO 8601).
//   - amountRUB — сумма в рублях (0 если неизвестна — не выводим).
//
// Ошибки логируются, но не возвращаются в активный поток webhook'а —
// если Telegram недоступен, оплата всё равно учтена.
func (t *Telegram) NotifyPaid(ctx context.Context, chatID int64, planName, expiresAtRFC3339 string, amountRUB float64) {
	if t == nil {
		return
	}

	expiresHuman := formatExpires(expiresAtRFC3339)

	var amountLine string
	if amountRUB > 0 {
		amountLine = fmt.Sprintf("\n💳 Сумма: <b>%s ₽</b>", formatRub(amountRUB))
	}

	text := fmt.Sprintf(
		"✅ <b>Оплата прошла!</b>\n\n"+
			"Подписка активирована: <b>%s</b>%s\n"+
			"⏳ Действует до: <b>%s</b>\n\n"+
			"Открой Mini App, чтобы получить ключ подключения 👇",
		htmlEscape(planName),
		amountLine,
		htmlEscape(expiresHuman),
	)

	if err := t.sendMessage(ctx, chatID, text); err != nil {
		t.logger.Warn("telegram notify paid failed",
			zap.Int64("chat_id", chatID),
			zap.Error(err),
		)
		return
	}
	t.logger.Info("telegram notify paid sent", zap.Int64("chat_id", chatID))
}

// NotifyChargeback — алерт об chargeback'е (CHARGEBACKED). Отправляется
// тому же юзеру: подписка деактивирована, деньги вернулись.
func (t *Telegram) NotifyChargeback(ctx context.Context, chatID int64) {
	if t == nil {
		return
	}
	text := "⚠️ <b>Платёж был отменён банком</b>\n\n" +
		"Деньги вернулись на твою карту, доступ к VPN деактивирован.\n" +
		"Если это ошибка — напиши в поддержку."

	if err := t.sendMessage(ctx, chatID, text); err != nil {
		t.logger.Warn("telegram notify chargeback failed",
			zap.Int64("chat_id", chatID),
			zap.Error(err),
		)
	}
}

// sendMessage — обёртка над Bot API sendMessage.
func (t *Telegram) sendMessage(ctx context.Context, chatID int64, text string) error {
	url := fmt.Sprintf(apiURLFormat, t.botToken, "sendMessage")

	body, err := json.Marshal(map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
		// disable_web_page_preview не критичен для нашего текста, но пусть будет —
		// меньше шансов что Telegram развернёт случайную ссылку.
		"disable_web_page_preview": true,
	})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var parsed struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	if !parsed.OK {
		return errors.New(parsed.Description)
	}
	return nil
}

// formatExpires — превращает RFC3339 timestamp ("2026-04-28T17:00:00Z")
// в человекочитаемое "28.04.2026 17:00" (UTC). Если парс не удался —
// возвращает исходную строку (лучше показать что-то, чем ничего).
func formatExpires(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("02.01.2006 15:04 UTC")
}

// formatRub — "499.00" → "499", "499.50" → "499.50".
func formatRub(amount float64) string {
	if amount == float64(int64(amount)) {
		return fmt.Sprintf("%d", int64(amount))
	}
	return fmt.Sprintf("%.2f", amount)
}

// htmlEscape — минимальный escape для HTML parse_mode у Telegram.
// Достаточно экранировать `<`, `>`, `&` (см. core.telegram.org/bots/api#html-style).
func htmlEscape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			out = append(out, "&lt;"...)
		case '>':
			out = append(out, "&gt;"...)
		case '&':
			out = append(out, "&amp;"...)
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
