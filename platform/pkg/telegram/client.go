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
