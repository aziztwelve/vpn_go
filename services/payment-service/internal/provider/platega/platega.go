// Package platega — провайдер оплаты Platega.io.
//
// Поддерживает два режима создания платежа:
//   - DefaultMethod == 0 → POST /v2/transaction/process
//     (без paymentMethod, юзер выбирает на форме Platega: СБП / карты / ЕРИП / crypto).
//   - DefaultMethod ∈ {2,3,11,12,13} → POST /transaction/process
//     с фиксированным paymentMethod (2=СБП QR, 3=ЕРИП, 11=Карты РФ, 12=Карты Intl,
//     13=Crypto).
//
// Авторизация — два заголовка: X-MerchantId (UUID) + X-Secret (API-ключ).
// У callback'а нет подписи payload — только сравнение тех же двух заголовков
// через subtle.ConstantTimeCompare. См. docs/services/platega-integration.md.
package platega

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vpn/payment-service/internal/provider"
	"go.uber.org/zap"
)

// Config — настройки провайдера Platega.
type Config struct {
	BaseURL       string // https://app.platega.io
	MerchantID    string // UUID из ЛК Platega
	APISecret     string // API-ключ (X-Secret)
	SuccessURL    string // куда редиректит юзера после успешной оплаты
	FailURL       string // куда редиректит после неуспешной
	DefaultMethod int    // 0 = v2 (без метода); 2/3/11/12/13 = v1 с конкретным методом
	Logger        *zap.Logger
}

// PlategaProvider — провайдер для Platega.io API.
type PlategaProvider struct {
	baseURL       string
	merchantID    string
	apiSecret     string
	successURL    string
	failURL       string
	defaultMethod int

	client *http.Client
	logger *zap.Logger
}

// NewProvider создаёт нового Platega-провайдера.
func NewProvider(cfg Config) *PlategaProvider {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PlategaProvider{
		baseURL:       strings.TrimRight(cfg.BaseURL, "/"),
		merchantID:    cfg.MerchantID,
		apiSecret:     cfg.APISecret,
		successURL:    cfg.SuccessURL,
		failURL:       cfg.FailURL,
		defaultMethod: cfg.DefaultMethod,
		client:        &http.Client{Timeout: 30 * time.Second},
		logger:        logger,
	}
}

// Name — имя провайдера, под которым он зарегистрирован в payment-service.
func (p *PlategaProvider) Name() string { return "platega" }

// ─────────────────────────────────────────────────────────────────────────────
// CreateInvoice
// ─────────────────────────────────────────────────────────────────────────────

// orderPayload — поле `payload` в запросе/callback'е Platega. Прокидывается
// без изменений; сюда кладём наш закодированный идентификатор платежа,
// совместимый с форматом WATA `orderId`, чтобы единообразно парсить
// callback в HandleWebhook.
//
// Формат: payment_<userID>_<planID>_<maxDevices>_<unix>
const orderPrefix = "payment_"

func buildPayload(userID int64, planID, maxDevices int32, ts int64) string {
	return fmt.Sprintf("%s%d_%d_%d_%d", orderPrefix, userID, planID, maxDevices, ts)
}

// createTxnRequest — request body для POST /v2/transaction/process и POST /transaction/process.
type createTxnRequest struct {
	PaymentMethod  *int           `json:"paymentMethod,omitempty"`
	PaymentDetails paymentDetails `json:"paymentDetails"`
	Description    string         `json:"description"`
	Return         string         `json:"return,omitempty"`
	FailedURL      string         `json:"failedUrl,omitempty"`
	Payload        string         `json:"payload,omitempty"`
}

type paymentDetails struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// createTxnResponse — общий response для v2 и v1. В v2 платёжный URL — это
// поле `url`, а в v1 — `redirect`. Поддерживаем оба и берём непустое.
type createTxnResponse struct {
	TransactionID string `json:"transactionId"`
	Status        string `json:"status"`
	URL           string `json:"url"`      // v2
	Redirect      string `json:"redirect"` // v1
	ExpiresIn     string `json:"expiresIn"`
}

// CreateInvoice — создаёт транзакцию в Platega и возвращает ссылку для оплаты.
func (p *PlategaProvider) CreateInvoice(ctx context.Context, req *provider.CreateInvoiceRequest) (*provider.Invoice, error) {
	if req.AmountRUB <= 0 {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_amount",
			Message:  fmt.Sprintf("amount_rub must be > 0, got %.2f", req.AmountRUB),
		}
	}

	body := createTxnRequest{
		PaymentDetails: paymentDetails{Amount: req.AmountRUB, Currency: "RUB"},
		Description:    req.Description,
		Return:         p.successURL,
		FailedURL:      p.failURL,
		Payload:        buildPayload(req.UserID, req.PlanID, req.MaxDevices, time.Now().Unix()),
	}

	var path string
	if p.defaultMethod == 0 {
		path = "/v2/transaction/process"
	} else {
		path = "/transaction/process"
		m := p.defaultMethod
		body.PaymentMethod = &m
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "marshal_error",
			Message: "failed to marshal request", Err: err,
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "request_error",
			Message: "failed to create request", Err: err,
		}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-MerchantId", p.merchantID)
	httpReq.Header.Set("X-Secret", p.apiSecret)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "api_error",
			Message: "failed to call platega api", Err: err,
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "read_error",
			Message: "failed to read response", Err: err,
		}
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "api_error",
			Message: fmt.Sprintf("platega api error: status=%d body=%s", resp.StatusCode, string(respBody)),
		}
	}

	var parsed createTxnResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "unmarshal_error",
			Message: "failed to unmarshal response", Err: err,
		}
	}

	link := parsed.URL
	if link == "" {
		link = parsed.Redirect
	}
	if parsed.TransactionID == "" || link == "" {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "invalid_response",
			Message: fmt.Sprintf("platega response missing transactionId or url: %s", string(respBody)),
		}
	}

	// expiresIn у Platega — фикс 15 минут, но парсим формат HH:MM:SS на случай изменения.
	expiresAt := time.Now().Add(15 * time.Minute)
	if d, ok := parseHHMMSS(parsed.ExpiresIn); ok {
		expiresAt = time.Now().Add(d)
	}

	p.logger.Info("platega invoice created",
		zap.Int64("user_id", req.UserID),
		zap.Int32("plan_id", req.PlanID),
		zap.Int32("max_devices", req.MaxDevices),
		zap.Float64("amount_rub", req.AmountRUB),
		zap.String("transaction_id", parsed.TransactionID),
		zap.String("payload", body.Payload),
		zap.Int("default_method", p.defaultMethod),
	)

	return &provider.Invoice{
		ExternalID:  parsed.TransactionID,
		InvoiceLink: link,
		Amount:      req.AmountRUB,
		Currency:    "RUB",
		ExpiresAt:   expiresAt,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPaymentStatus
// ─────────────────────────────────────────────────────────────────────────────

// GetPaymentStatus — GET /transaction/{id}, fallback к callback'у.
func (p *PlategaProvider) GetPaymentStatus(ctx context.Context, externalID string) (*provider.PaymentStatus, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/transaction/"+externalID, nil)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "request_error",
			Message: "failed to create request", Err: err,
		}
	}
	httpReq.Header.Set("X-MerchantId", p.merchantID)
	httpReq.Header.Set("X-Secret", p.apiSecret)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "api_error",
			Message: "failed to get payment status", Err: err,
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "read_error",
			Message: "failed to read response", Err: err,
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "api_error",
			Message: fmt.Sprintf("platega api error: status=%d body=%s", resp.StatusCode, string(respBody)),
		}
	}

	var parsed struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "unmarshal_error",
			Message: "failed to unmarshal response", Err: err,
		}
	}

	return &provider.PaymentStatus{
		ExternalID: externalID,
		Status:     mapStatus(parsed.Status),
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Webhook
// ─────────────────────────────────────────────────────────────────────────────

// callbackPayload — то, что Platega шлёт в теле callback'а.
type callbackPayload struct {
	ID            string  `json:"id"`            // UUID транзакции
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	Status        string  `json:"status"`        // CONFIRMED | CANCELED | CHARGEBACKED
	PaymentMethod int     `json:"paymentMethod"` // 2/3/11/12/13
	Payload       string  `json:"payload"`       // наш `payment_<u>_<p>_<d>_<t>`
}

// ValidateWebhook — у Platega нет криптоподписи payload. Аутентификация
// callback'а основана на сравнении заголовков X-MerchantId / X-Secret
// с конфигом. Gateway передаёт оба значения склеенными через `:`,
// см. services/gateway/internal/handler/payment.go (case "platega").
//
// Сравнение через subtle.ConstantTimeCompare — защита от timing-атак при
// подборе секрета.
func (p *PlategaProvider) ValidateWebhook(payload []byte, signature string) error {
	if signature == "" {
		return &provider.ProviderError{
			Provider: p.Name(), Code: "missing_credentials",
			Message: "platega callback: X-MerchantId / X-Secret headers are empty",
		}
	}
	mid, sec, ok := strings.Cut(signature, ":")
	if !ok {
		return &provider.ProviderError{
			Provider: p.Name(), Code: "invalid_credentials_format",
			Message: "platega callback: signature must be 'merchantId:secret'",
		}
	}

	midOK := subtle.ConstantTimeCompare([]byte(mid), []byte(p.merchantID)) == 1
	secOK := subtle.ConstantTimeCompare([]byte(sec), []byte(p.apiSecret)) == 1
	if !midOK || !secOK {
		return &provider.ProviderError{
			Provider: p.Name(), Code: "invalid_credentials",
			Message: "platega callback: X-MerchantId / X-Secret mismatch",
		}
	}
	return nil
}

// HandleWebhook — парсит callback, валидирует подпись (заголовки), вынимает
// userID/planID/maxDevices из поля payload и возвращает WebhookEvent.
func (p *PlategaProvider) HandleWebhook(ctx context.Context, payload []byte, signature string) (*provider.WebhookEvent, error) {
	p.logger.Info("platega webhook RAW payload", zap.Int("len", len(payload)))

	if err := p.ValidateWebhook(payload, signature); err != nil {
		// Логируем но возвращаем ошибку — gateway сам решит, что отвечать
		// (по доке Platega всё равно лучше отвечать 200 чтобы не получить ретраи,
		// но семантически некорректные креды должны логироваться как инцидент).
		p.logger.Warn("platega webhook: invalid credentials")
		return nil, err
	}

	var cb callbackPayload
	if err := json.Unmarshal(payload, &cb); err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "invalid_payload",
			Message: "failed to unmarshal webhook", Err: err,
		}
	}

	userID, planID, maxDevices, err := parsePayload(cb.Payload)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(), Code: "invalid_payload_field",
			Message: fmt.Sprintf("failed to parse payload: %s", cb.Payload), Err: err,
		}
	}

	mappedStatus := mapStatus(cb.Status)

	metadata := map[string]string{
		"transaction_id": cb.ID,
		"payload":        cb.Payload,
		"amount":         strconv.FormatFloat(cb.Amount, 'f', 2, 64),
		"currency":       cb.Currency,
		"payment_method": strconv.Itoa(cb.PaymentMethod),
		"status_raw":     cb.Status,
	}

	p.logger.Info("platega webhook received",
		zap.String("transaction_id", cb.ID),
		zap.String("payload", cb.Payload),
		zap.String("status_raw", cb.Status),
		zap.String("status_mapped", mappedStatus),
		zap.Float64("amount", cb.Amount),
		zap.Int("payment_method", cb.PaymentMethod),
	)

	return &provider.WebhookEvent{
		ExternalID: cb.ID,
		Status:     mappedStatus,
		UserID:     userID,
		PlanID:     planID,
		MaxDevices: maxDevices,
		Metadata:   metadata,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

// mapStatus — маппит PaymentStatus enum Platega на наш внутренний статус.
//
//	Platega        →  Внутренний (model.Status*)
//	PENDING        →  pending
//	CONFIRMED      →  paid
//	CANCELED       →  cancelled
//	CHARGEBACKED   →  refunded
func mapStatus(plategaStatus string) string {
	switch strings.ToUpper(plategaStatus) {
	case "CONFIRMED":
		return "paid"
	case "CANCELED", "CANCELLED":
		return "cancelled"
	case "CHARGEBACKED":
		return "refunded"
	case "PENDING":
		return "pending"
	default:
		return "pending"
	}
}

// parsePayload — обратная функция к buildPayload.
//
// Формат: payment_<userID>_<planID>_<maxDevices>_<unix>
func parsePayload(s string) (userID int64, planID int32, maxDevices int32, err error) {
	if !strings.HasPrefix(s, orderPrefix) {
		return 0, 0, 0, fmt.Errorf("payload must start with %q", orderPrefix)
	}
	rest := strings.TrimPrefix(s, orderPrefix)
	parts := strings.Split(rest, "_")
	if len(parts) != 4 {
		return 0, 0, 0, fmt.Errorf("payload must have 4 segments after prefix, got %d", len(parts))
	}
	uid, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse userID: %w", err)
	}
	pid, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse planID: %w", err)
	}
	dev, err := strconv.ParseInt(parts[2], 10, 32)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse maxDevices: %w", err)
	}
	if _, err := strconv.ParseInt(parts[3], 10, 64); err != nil {
		return 0, 0, 0, fmt.Errorf("parse unix ts: %w", err)
	}
	return uid, int32(pid), int32(dev), nil
}

// parseHHMMSS — парсит "HH:MM:SS" в time.Duration.
func parseHHMMSS(s string) (time.Duration, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec)*time.Second, true
}
