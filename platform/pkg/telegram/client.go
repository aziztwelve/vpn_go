// Package telegram — минимальный HTTP-клиент над Telegram Bot API,
// только методы, нужные для Stars-платежей. Полный Bot API большой —
// тащим его по мере необходимости, не сразу.
//
// Дока: https://core.telegram.org/bots/api#payments
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const DefaultBaseURL = "https://api.telegram.org"

type Client struct {
	baseURL  string
	token    string
	http     *http.Client
}

func New(token string) *Client {
	return &Client{
		baseURL: DefaultBaseURL,
		token:   token,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// apiResponse — общий wrapper Telegram Bot API.
type apiResponse struct {
	Ok          bool            `json:"ok"`
	Description string          `json:"description,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

func (c *Client) call(ctx context.Context, method string, body any, out any) error {
	url := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", method, err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, reqBody)
	if err != nil {
		return fmt.Errorf("new request %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("do %s: %w", method, err)
	}
	defer resp.Body.Close()

	var api apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&api); err != nil {
		return fmt.Errorf("decode %s: %w", method, err)
	}
	if !api.Ok {
		return fmt.Errorf("telegram %s error %d: %s", method, api.ErrorCode, api.Description)
	}
	if out != nil && len(api.Result) > 0 {
		if err := json.Unmarshal(api.Result, out); err != nil {
			return fmt.Errorf("unmarshal result %s: %w", method, err)
		}
	}
	return nil
}

// LabeledPrice — единица цены в инвойсе.
type LabeledPrice struct {
	Label  string `json:"label"`
	Amount int32  `json:"amount"`
}

type CreateInvoiceLinkParams struct {
	Title         string         `json:"title"`
	Description   string         `json:"description"`
	Payload       string         `json:"payload"` // internal identifier (используем как payment_id)
	ProviderToken string         `json:"provider_token,omitempty"` // "" для Telegram Stars
	Currency      string         `json:"currency"` // "XTR" для Stars
	Prices        []LabeledPrice `json:"prices"`
}

// CreateInvoiceLink возвращает t.me/$... ссылку для Mini App.openInvoice().
// https://core.telegram.org/bots/api#createinvoicelink
func (c *Client) CreateInvoiceLink(ctx context.Context, params CreateInvoiceLinkParams) (string, error) {
	var link string
	if err := c.call(ctx, "createInvoiceLink", params, &link); err != nil {
		return "", err
	}
	return link, nil
}

type AnswerPreCheckoutQueryParams struct {
	PreCheckoutQueryID string `json:"pre_checkout_query_id"`
	Ok                 bool   `json:"ok"`
	ErrorMessage       string `json:"error_message,omitempty"` // required if Ok=false
}

// AnswerPreCheckoutQuery — обязательный ответ в течение 10с на
// pre_checkout_query update. Без него Telegram не списывает Stars.
// https://core.telegram.org/bots/api#answerprecheckoutquery
func (c *Client) AnswerPreCheckoutQuery(ctx context.Context, params AnswerPreCheckoutQueryParams) error {
	return c.call(ctx, "answerPreCheckoutQuery", params, nil)
}

type RefundStarPaymentParams struct {
	UserID                  int64  `json:"user_id"`
	TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
}

// RefundStarPayment — возврат Stars в течение 21 дня.
// https://core.telegram.org/bots/api#refundstarpayment
func (c *Client) RefundStarPayment(ctx context.Context, params RefundStarPaymentParams) error {
	return c.call(ctx, "refundStarPayment", params, nil)
}

// ============================================================
// Messaging / UI (bot commands, welcome, menu-button)
// ============================================================

// WebAppInfo — ссылка на Mini App; используется в InlineKeyboardButton.web_app
// и в MenuButton (type=web_app). Telegram открывает её прямо в in-app WebView.
// https://core.telegram.org/bots/api#webappinfo
type WebAppInfo struct {
	URL string `json:"url"`
}

// InlineKeyboardButton — одна из кнопок в инлайн-клавиатуре под сообщением.
// Поддерживаем только подмножество: url + web_app; callback_data/switch_inline
// не нужны для приветственного сообщения.
// https://core.telegram.org/bots/api#inlinekeyboardbutton
type InlineKeyboardButton struct {
	Text   string      `json:"text"`
	URL    string      `json:"url,omitempty"`
	WebApp *WebAppInfo `json:"web_app,omitempty"`
}

// InlineKeyboardMarkup — двумерный массив кнопок (строки × колонки).
// https://core.telegram.org/bots/api#inlinekeyboardmarkup
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// SendMessageParams — параметры sendMessage. Не-используемые поля опущены —
// добавим по мере надобности.
// https://core.telegram.org/bots/api#sendmessage
type SendMessageParams struct {
	ChatID      int64                 `json:"chat_id"`
	Text        string                `json:"text"`
	ParseMode   string                `json:"parse_mode,omitempty"` // "HTML" | "Markdown" | "MarkdownV2"
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	// LinkPreviewOptions.is_disabled = true — чтобы приветствие не тащило
	// превью картинки с cdn.*. Для простоты используем legacy-поле.
	DisableWebPagePreview bool `json:"disable_web_page_preview,omitempty"`
}

// SendMessage — послать текст юзеру/чату.
func (c *Client) SendMessage(ctx context.Context, params SendMessageParams) error {
	return c.call(ctx, "sendMessage", params, nil)
}

// MenuButton — объект chat-menu-button для setChatMenuButton.
// Поддерживаем только web_app (остальные варианты: default, commands).
// https://core.telegram.org/bots/api#menubutton
type MenuButton struct {
	Type   string      `json:"type"`             // "web_app" | "default" | "commands"
	Text   string      `json:"text,omitempty"`   // только для web_app
	WebApp *WebAppInfo `json:"web_app,omitempty"` // только для web_app
}

// SetChatMenuButtonParams — если ChatID = 0 (omitempty), выставляется
// дефолтная menu-button для всех пользователей бота.
// https://core.telegram.org/bots/api#setchatmenubutton
type SetChatMenuButtonParams struct {
	ChatID     int64       `json:"chat_id,omitempty"`
	MenuButton *MenuButton `json:"menu_button"`
}

// SetChatMenuButton — выставить кнопку слева от поля ввода.
// Без chat_id — применяется глобально (default). С chat_id — только этому чату.
func (c *Client) SetChatMenuButton(ctx context.Context, params SetChatMenuButtonParams) error {
	return c.call(ctx, "setChatMenuButton", params, nil)
}

// SetWebhookParams — регистрация/обновление URL вебхука.
// https://core.telegram.org/bots/api#setwebhook
type SetWebhookParams struct {
	URL            string   `json:"url"`
	SecretToken    string   `json:"secret_token,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
	DropPendingUpdates bool `json:"drop_pending_updates,omitempty"`
}

// SetWebhook — регистрирует вебхук. Вызывается one-off из deploy-скрипта.
func (c *Client) SetWebhook(ctx context.Context, params SetWebhookParams) error {
	return c.call(ctx, "setWebhook", params, nil)
}
