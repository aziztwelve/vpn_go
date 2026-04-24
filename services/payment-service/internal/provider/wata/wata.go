// Package wata — реализация PaymentProvider для WATA H2H API.
//
// Флоу:
//  1. CreateInvoice → POST /links → получаем uuid-ссылку и url → редирект юзера.
//     external_id = uuid ссылки. В orderId передаём payment.ID (для ручного
//     поиска в ЛК WATA, и как fallback идентификатор на случай если
//     payment_link удалится по TTL).
//  2. Webhook приходит на gateway → forwardится в payment-service.
//     Подпись — RSA-SHA512 в заголовке X-Signature (base64). Публичный
//     ключ берём из /public-key, кэшируем на 24 часа.
//  3. По transactionStatus маппим Paid → paid, Declined → failed.
//     event.ExternalID = n.ID (link UUID) — по нему repo.GetByExternalID.
//
// См. docs/services/wata-integration.md.
package wata

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vpn/payment-service/internal/provider"
	"go.uber.org/zap"
)

// Config — параметры провайдера. Валидируется в config.go; сюда приходит готовым.
type Config struct {
	BaseURL     string
	AccessToken string
	SuccessURL  string
	FailURL     string
	LinkTTL     time.Duration
	Logger      *zap.Logger
}

// Provider — реализация PaymentProvider для WATA.
type Provider struct {
	cfg        Config
	httpClient *http.Client

	// Кэш публичного ключа WATA (для верификации подписи webhook).
	// WATA не ротирует ключ часто, но мы рефрешим раз в сутки на всякий.
	pubKeyMu     sync.RWMutex
	pubKey       *rsa.PublicKey
	pubKeyExpiry time.Time
}

// Срок жизни кэша публичного ключа. Раз в сутки перечитываем.
const pubKeyCacheTTL = 24 * time.Hour

// NewProvider создаёт WATA-провайдер. Публичный ключ НЕ фетчится сразу —
// ленивая инициализация при первом webhook (чтобы сервис поднимался даже
// если WATA временно недоступен).
func NewProvider(cfg Config) *Provider {
	return &Provider{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Name — идентификатор провайдера (используется в URL-query ?provider=wata,
// в колонке payments.provider и в /api/v1/payments/webhook/wata).
func (p *Provider) Name() string { return "wata" }

// ─────────────────────────────────────────────────────────────────────
// CreateInvoice
// ─────────────────────────────────────────────────────────────────────

// createLinkRequest — тело POST /links.
type createLinkRequest struct {
	Type               string  `json:"type"`                         // "OneTime"
	Amount             float64 `json:"amount"`                       // RUB
	Currency           string  `json:"currency"`                     // "RUB"
	Description        string  `json:"description,omitempty"`
	OrderID            string  `json:"orderId,omitempty"`            // наш payment.ID как string
	SuccessRedirectURL string  `json:"successRedirectUrl,omitempty"`
	FailRedirectURL    string  `json:"failRedirectUrl,omitempty"`
	ExpirationDateTime string  `json:"expirationDateTime,omitempty"` // RFC3339 UTC
}

// createLinkResponse — ответ POST /links.
type createLinkResponse struct {
	ID                 string `json:"id"`   // UUID платёжной ссылки — наш ExternalID
	URL                string `json:"url"`  // куда редиректим юзера
	Status             string `json:"status"`
	ExpirationDateTime string `json:"expirationDateTime"`
}

// CreateInvoice создаёт платёжную ссылку в WATA.
func (p *Provider) CreateInvoice(ctx context.Context, req *provider.CreateInvoiceRequest) (*provider.Invoice, error) {
	if req.AmountRUB <= 0 {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_amount",
			Message:  fmt.Sprintf("amount_rub must be > 0, got %.2f", req.AmountRUB),
		}
	}

	// orderId — наш payment.ID. service/payment.go кладёт его в Metadata["payment_id"].
	orderID := req.Metadata["payment_id"]

	body := createLinkRequest{
		Type:               "OneTime",
		Amount:             req.AmountRUB,
		Currency:           "RUB",
		Description:        req.Description,
		OrderID:            orderID,
		SuccessRedirectURL: p.cfg.SuccessURL,
		FailRedirectURL:    p.cfg.FailURL,
		ExpirationDateTime: time.Now().UTC().Add(p.cfg.LinkTTL).Format(time.RFC3339),
	}

	var resp createLinkResponse
	if err := p.doJSON(ctx, http.MethodPost, "/links", body, &resp); err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "create_link_failed",
			Message:  "failed to create payment link",
			Err:      err,
		}
	}

	if resp.ID == "" || resp.URL == "" {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_response",
			Message:  fmt.Sprintf("wata returned empty id/url: %+v", resp),
		}
	}

	p.cfg.Logger.Info("wata invoice created",
		zap.Int64("user_id", req.UserID),
		zap.Int32("plan_id", req.PlanID),
		zap.Float64("amount_rub", req.AmountRUB),
		zap.String("order_id", orderID),
		zap.String("link_id", resp.ID),
	)

	// Парсим expiry — если не получится, берём now + LinkTTL.
	expiresAt := time.Now().Add(p.cfg.LinkTTL)
	if t, err := time.Parse(time.RFC3339, resp.ExpirationDateTime); err == nil {
		expiresAt = t
	}

	return &provider.Invoice{
		ExternalID:  resp.ID,
		InvoiceLink: resp.URL,
		Amount:      req.AmountRUB,
		Currency:    "RUB",
		ExpiresAt:   expiresAt,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────
// GetPaymentStatus — для manual/fallback-сценария
// ─────────────────────────────────────────────────────────────────────

// getTxResponse — ответ GET /transactions/{id}. Нужны не все поля.
type getTxResponse struct {
	ID         string `json:"id"`
	Status     string `json:"status"`     // Created|Pending|Paid|Declined
	PaymentTime string `json:"paymentTime"`
}

// GetPaymentStatus не рекомендуется для polling (rate limit 1/30s) —
// опираемся на webhook. Оставлена для manual-проверки админом.
func (p *Provider) GetPaymentStatus(ctx context.Context, externalID string) (*provider.PaymentStatus, error) {
	// externalID у нас = UUID платёжной ссылки. Ищем её транзакции через
	// /transactions?paymentLinkIds=[uuid] — но проще поискать по orderId
	// если знать payment.ID. Здесь идём по UUID ссылки:
	var resp struct {
		Items []getTxResponse `json:"items"`
	}
	path := fmt.Sprintf("/transactions?paymentLinkIds=%s", externalID)
	if err := p.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "get_status_failed",
			Message:  "failed to query transactions",
			Err:      err,
		}
	}

	if len(resp.Items) == 0 {
		return &provider.PaymentStatus{ExternalID: externalID, Status: "pending"}, nil
	}

	// Берём первую Paid, иначе последнюю (массив отсортирован по creationTime desc при дефолтной сортировке).
	var tx *getTxResponse
	for i := range resp.Items {
		if resp.Items[i].Status == "Paid" {
			tx = &resp.Items[i]
			break
		}
	}
	if tx == nil {
		tx = &resp.Items[0]
	}

	status := mapTransactionStatus(tx.Status)
	ps := &provider.PaymentStatus{ExternalID: externalID, Status: status}
	if t, err := time.Parse(time.RFC3339, tx.PaymentTime); err == nil && status == "paid" {
		ps.PaidAt = &t
	}
	return ps, nil
}

// ─────────────────────────────────────────────────────────────────────
// Webhook
// ─────────────────────────────────────────────────────────────────────

// webhookPayload — payload webhook от WATA.
// См. docs/services/wata-integration.md "Пример payload".
type webhookPayload struct {
	TransactionType       string  `json:"transactionType"`    // CardCrypto | SBP | TPay | SberPay
	Kind                  string  `json:"kind"`               // Payment | Refund
	ID                    string  `json:"id"`                 // UUID платёжной ссылки
	TransactionID         string  `json:"transactionId"`
	OriginalTransactionID string  `json:"originalTransactionId"`
	TransactionStatus     string  `json:"transactionStatus"`  // Created | Pending | Paid | Declined
	TerminalPublicID      string  `json:"terminalPublicId"`
	TerminalName          string  `json:"terminalName"`
	Amount                float64 `json:"amount"`
	Currency              string  `json:"currency"`
	OrderID               string  `json:"orderId"`            // наш payment.ID как строка
	OrderDescription      string  `json:"orderDescription"`
	PaymentTime           string  `json:"paymentTime"`
	Commission            float64 `json:"commission"`
	Email                 string  `json:"email"`
	PaymentLinkID         string  `json:"paymentLinkId"`
	ErrorCode             string  `json:"errorCode"`
	ErrorDescription      string  `json:"errorDescription"`
}

// ValidateWebhook проверяет RSA-SHA512 подпись webhook.
// signature — значение заголовка X-Signature (base64).
func (p *Provider) ValidateWebhook(payload []byte, signature string) error {
	if signature == "" {
		return &provider.ProviderError{
			Provider: p.Name(),
			Code:     "missing_signature",
			Message:  "X-Signature header is empty",
		}
	}

	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_signature_encoding",
			Message:  "signature is not valid base64",
			Err:      err,
		}
	}

	// Получаем публичный ключ (из кэша или свежий).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pub, err := p.getPublicKey(ctx)
	if err != nil {
		return &provider.ProviderError{
			Provider: p.Name(),
			Code:     "public_key_fetch_failed",
			Message:  "failed to fetch wata public key",
			Err:      err,
		}
	}

	hash := sha512.Sum512(payload)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA512, hash[:], sig); err != nil {
		return &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_signature",
			Message:  "webhook signature verification failed",
			Err:      err,
		}
	}
	return nil
}

// HandleWebhook парсит payload и возвращает событие.
// Подпись уже проверена в ValidateWebhook (service/payment.go).
func (p *Provider) HandleWebhook(ctx context.Context, payload []byte, signature string) (*provider.WebhookEvent, error) {
	var n webhookPayload
	if err := json.Unmarshal(payload, &n); err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_payload",
			Message:  "failed to parse webhook json",
			Err:      err,
		}
	}

	// Поддерживаем только Payment-webhook пока. Refund будем добавлять
	// отдельно когда появится сценарий (UI/админка рефандов).
	if n.Kind != "Payment" {
		p.cfg.Logger.Info("wata webhook skipped (non-payment kind)",
			zap.String("kind", n.Kind),
			zap.String("transaction_id", n.TransactionID),
		)
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "unsupported_kind",
			Message:  fmt.Sprintf("only Payment kind is supported, got %s", n.Kind),
		}
	}

	if n.ID == "" {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_payload",
			Message:  "payload.id (payment link uuid) is empty",
		}
	}

	status := mapTransactionStatus(n.TransactionStatus)

	p.cfg.Logger.Info("wata webhook received",
		zap.String("kind", n.Kind),
		zap.String("transaction_id", n.TransactionID),
		zap.String("link_id", n.ID),
		zap.String("order_id", n.OrderID),
		zap.String("status", n.TransactionStatus),
		zap.Float64("amount", n.Amount),
		zap.String("currency", n.Currency),
	)

	return &provider.WebhookEvent{
		ExternalID: n.ID, // = UUID платёжной ссылки (совпадает с payment.external_id)
		Status:     status,
		// UserID/PlanID/MaxDevices service.go возьмёт из payment'а по ExternalID.
		Metadata: map[string]string{
			"transaction_id":    n.TransactionID,
			"transaction_type":  n.TransactionType,
			"order_id":          n.OrderID,
			"payment_time":      n.PaymentTime,
			"error_code":        n.ErrorCode,
			"error_description": n.ErrorDescription,
		},
	}, nil
}

// mapTransactionStatus переводит статус WATA в наш enum:
//   Paid     → "paid"
//   Declined → "failed"
//   остальное (Created/Pending) → "pending" (не трогаем payment в БД)
func mapTransactionStatus(s string) string {
	switch s {
	case "Paid":
		return "paid"
	case "Declined":
		return "failed"
	default:
		return "pending"
	}
}

// ─────────────────────────────────────────────────────────────────────
// HTTP-helpers
// ─────────────────────────────────────────────────────────────────────

// doJSON делает авторизованный JSON-запрос к WATA API.
// При 4xx/5xx возвращает ошибку с телом для диагностики.
func (p *Provider) doJSON(ctx context.Context, method, path string, body, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	url := strings.TrimRight(p.cfg.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.cfg.AccessToken)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("wata api %s %s: http %d: %s",
			method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("unmarshal response: %w (body: %s)", err, string(respBody))
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────
// Public key cache
// ─────────────────────────────────────────────────────────────────────

type publicKeyResponse struct {
	Value string `json:"value"` // PEM-encoded (PKCS1)
}

// getPublicKey возвращает публичный ключ WATA, кешируя его на pubKeyCacheTTL.
// Конкурентно-безопасно (RWMutex).
func (p *Provider) getPublicKey(ctx context.Context) (*rsa.PublicKey, error) {
	// Fast path — читаем из кэша под RLock.
	p.pubKeyMu.RLock()
	if p.pubKey != nil && time.Now().Before(p.pubKeyExpiry) {
		key := p.pubKey
		p.pubKeyMu.RUnlock()
		return key, nil
	}
	p.pubKeyMu.RUnlock()

	// Slow path — фетчим и парсим под Lock.
	p.pubKeyMu.Lock()
	defer p.pubKeyMu.Unlock()

	// Double-check — вдруг уже обновили пока ждали Lock.
	if p.pubKey != nil && time.Now().Before(p.pubKeyExpiry) {
		return p.pubKey, nil
	}

	var resp publicKeyResponse
	if err := p.doJSON(ctx, http.MethodGet, "/public-key", nil, &resp); err != nil {
		return nil, fmt.Errorf("fetch public key: %w", err)
	}

	key, err := parsePKCS1PublicKey(resp.Value)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}

	p.pubKey = key
	p.pubKeyExpiry = time.Now().Add(pubKeyCacheTTL)
	p.cfg.Logger.Info("wata public key refreshed",
		zap.Time("expires_at", p.pubKeyExpiry),
	)
	return key, nil
}

// parsePKCS1PublicKey декодирует PEM-блок и парсит его как PKCS1 public key
// (так отдаёт WATA).
func parsePKCS1PublicKey(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("not a valid PEM block")
	}

	// WATA возвращает "PUBLIC KEY" header, но содержимое — PKCS1
	// (проверено в документации). Сначала пробуем PKCS1, потом PKIX
	// как fallback на случай если формат поменяется.
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse pkcs1/pkix: %w", err)
	}
	rsaKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("key is not rsa: got %T", pub)
	}
	return rsaKey, nil
}
