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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/vpn/payment-service/internal/provider"
	"go.uber.org/zap"
)

// publicKeyTTL — как часто перезапрашиваем публичный ключ WATA.
// Ключ редко меняется, но WATA рекомендует periodically refresh.
const publicKeyTTL = 24 * time.Hour

// Config — конфигурация Wata провайдера.
type Config struct {
	BaseURL     string
	AccessToken string
	SuccessURL  string
	FailURL     string
	LinkTTL     time.Duration
	Logger      *zap.Logger
}

// WataProvider — провайдер для Wata.pro.
type WataProvider struct {
	baseURL     string
	accessToken string
	successURL  string
	failURL     string
	linkTTL     time.Duration
	client      *http.Client
	logger      *zap.Logger

	// Кэш публичного ключа для проверки RSA-подписи webhook'ов.
	// Защищён mu; обновляется по TTL или при ошибке верификации.
	mu             sync.RWMutex
	publicKey      *rsa.PublicKey
	publicKeyAt    time.Time
}

// NewProvider создаёт новый Wata.pro провайдер.
func NewProvider(cfg Config) *WataProvider {
	return &WataProvider{
		baseURL:     cfg.BaseURL,
		accessToken: cfg.AccessToken,
		successURL:  cfg.SuccessURL,
		failURL:     cfg.FailURL,
		linkTTL:     cfg.LinkTTL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: cfg.Logger,
	}
}

// Name возвращает имя провайдера.
func (p *WataProvider) Name() string {
	return "wata"
}

// CreateInvoice создаёт платёж через Wata.pro.
func (p *WataProvider) CreateInvoice(ctx context.Context, req *provider.CreateInvoiceRequest) (*provider.Invoice, error) {
	if req.AmountRUB <= 0 {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_amount",
			Message:  fmt.Sprintf("amount_rub must be > 0, got %.2f", req.AmountRUB),
		}
	}

	// Генерируем уникальный orderId
	orderId := fmt.Sprintf("payment_%d_%d_%d_%d", req.UserID, req.PlanID, req.MaxDevices, time.Now().Unix())

	// Формируем запрос к Wata API.
	//
	// type=ManyTime — по требованию техподдержки WATA. Семантически для
	// одноразовой оплаты тарифа корректнее OneTime, но их sandbox-терминал
	// `Maydavpnbot_test` отказывал в TRA_2999 даже на тестовой карте; поддержка
	// прислала формат именно с ManyTime. После того как они починят терминал —
	// стоит вернуть OneTime, чтобы исключить возможность повторных списаний
	// по той же ссылке.
	wataReq := map[string]interface{}{
		"type":               "ManyTime",
		"amount":             req.AmountRUB,
		"currency":           "RUB",
		"description":        req.Description,
		"orderId":            orderId,
		"successRedirectUrl": p.successURL,
		"failRedirectUrl":    p.failURL,
		"expirationDateTime": time.Now().Add(p.linkTTL).UTC().Format("2006-01-02T15:04:05.000Z"),
	}

	body, err := json.Marshal(wataReq)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "marshal_error",
			Message:  "failed to marshal request",
			Err:      err,
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/links", bytes.NewReader(body))
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "request_error",
			Message:  "failed to create request",
			Err:      err,
		}
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.accessToken)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "api_error",
			Message:  "failed to call wata api",
			Err:      err,
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "read_error",
			Message:  "failed to read response",
			Err:      err,
		}
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "api_error",
			Message:  fmt.Sprintf("wata api error: status=%d body=%s", resp.StatusCode, string(respBody)),
		}
	}

	var wataResp struct {
		LinkId  string  `json:"id"`
		Url     string  `json:"url"`
		Amount  float64 `json:"amount"`
		OrderId string  `json:"orderId"`
		Status  string  `json:"status"`
	}

	if err := json.Unmarshal(respBody, &wataResp); err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "unmarshal_error",
			Message:  "failed to unmarshal response",
			Err:      err,
		}
	}

	p.logger.Info("wata invoice created",
		zap.Int64("user_id", req.UserID),
		zap.Int32("plan_id", req.PlanID),
		zap.Float64("amount_rub", req.AmountRUB),
		zap.String("link_id", wataResp.LinkId),
		zap.String("order_id", wataResp.OrderId),
	)

	return &provider.Invoice{
		ExternalID:  wataResp.LinkId,
		InvoiceLink: wataResp.Url,
		Amount:      req.AmountRUB,
		Currency:    "RUB",
		ExpiresAt:   time.Now().Add(p.linkTTL),
	}, nil
}

// GetPaymentStatus проверяет статус платежа.
func (p *WataProvider) GetPaymentStatus(ctx context.Context, externalID string) (*provider.PaymentStatus, error) {
	// Wata.pro поддерживает проверку статуса через GET /api/h2h/links/{linkId}
	httpReq, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/links/"+externalID, nil)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "request_error",
			Message:  "failed to create request",
			Err:      err,
		}
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.accessToken)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "api_error",
			Message:  "failed to get payment status",
			Err:      err,
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "read_error",
			Message:  "failed to read response",
			Err:      err,
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "api_error",
			Message:  fmt.Sprintf("wata api error: status=%d body=%s", resp.StatusCode, string(respBody)),
		}
	}

	var wataResp struct {
		Status string `json:"status"`
	}

	if err := json.Unmarshal(respBody, &wataResp); err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "unmarshal_error",
			Message:  "failed to unmarshal response",
			Err:      err,
		}
	}

	return &provider.PaymentStatus{
		ExternalID: externalID,
		Status:     wataResp.Status,
	}, nil
}

// HandleWebhook обрабатывает webhook от Wata.pro.
func (p *WataProvider) HandleWebhook(ctx context.Context, payload []byte, signature string) (*provider.WebhookEvent, error) {
	// Логируем RAW payload для отладки
	p.logger.Info("wata webhook RAW payload", zap.String("payload", string(payload)))

	var webhook struct {
		LinkId             string  `json:"paymentLinkId"`      // Wata использует paymentLinkId
		OrderId            string  `json:"orderId"`
		TransactionStatus  string  `json:"transactionStatus"`  // Wata использует transactionStatus вместо status
		Amount             float64 `json:"amount"`
		Currency           string  `json:"currency"`
		PaymentTime        *string `json:"paymentTime"`        // Может быть null
		TransactionId      string  `json:"transactionId"`
	}

	if err := json.Unmarshal(payload, &webhook); err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_payload",
			Message:  "failed to unmarshal webhook",
			Err:      err,
		}
	}

	// Парсим orderId для получения данных платежа
	// Формат: payment_{user_id}_{plan_id}_{max_devices}_{timestamp}
	var userID, planID, maxDevices int64
	_, err := fmt.Sscanf(webhook.OrderId, "payment_%d_%d_%d_%d", &userID, &planID, &maxDevices, new(int64))
	if err != nil {
		return nil, &provider.ProviderError{
			Provider: p.Name(),
			Code:     "invalid_order_id",
			Message:  fmt.Sprintf("failed to parse orderId: %s", webhook.OrderId),
			Err:      err,
		}
	}

	// Формируем metadata
	var paymentDate string
	if webhook.PaymentTime != nil {
		paymentDate = *webhook.PaymentTime
	}

	metadata := map[string]string{
		"link_id":        webhook.LinkId,
		"order_id":       webhook.OrderId,
		"transaction_id": webhook.TransactionId,
		"amount":         strconv.FormatFloat(webhook.Amount, 'f', 2, 64),
		"currency":       webhook.Currency,
		"payment_time":   paymentDate,
	}

	// Маппим статус Wata на наш статус
	var status string
	switch webhook.TransactionStatus {
	case "Confirmed", "Authorized":
		status = "paid"
	case "Cancelled", "Declined":
		status = "cancelled"
	case "Created":
		status = "pending"
	default:
		status = "pending"
	}

	p.logger.Info("wata webhook received",
		zap.String("link_id", webhook.LinkId),
		zap.String("order_id", webhook.OrderId),
		zap.String("transaction_status", webhook.TransactionStatus),
		zap.String("mapped_status", status),
		zap.Float64("amount", webhook.Amount),
	)

	return &provider.WebhookEvent{
		ExternalID: webhook.LinkId,
		Status:     status,
		UserID:     userID,
		PlanID:     int32(planID),
		MaxDevices: int32(maxDevices),
		Metadata:   metadata,
	}, nil
}

// ValidateWebhook проверяет RSA-SHA512 подпись webhook от WATA.
//
// signature — base64-encoded RSA-SHA512 подпись raw body запроса
// (header X-Signature). Публичный ключ забираем из GET /public-key
// (PKCS1, PEM) и кешируем на publicKeyTTL.
//
// При несоответствии подписи возвращаем ошибку — gateway должен НЕ обрабатывать
// payload и логировать инцидент.
func (p *WataProvider) ValidateWebhook(payload []byte, signature string) error {
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
			Code:     "invalid_signature_b64",
			Message:  "X-Signature is not valid base64",
			Err:      err,
		}
	}

	pub, err := p.getPublicKey(context.Background(), false)
	if err != nil {
		return &provider.ProviderError{
			Provider: p.Name(),
			Code:     "public_key_unavailable",
			Message:  "failed to get wata public key",
			Err:      err,
		}
	}

	hash := sha512.Sum512(payload)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA512, hash[:], sig); err != nil {
		// Возможно, ключ ротировали — пробуем перезапросить и проверить ещё раз.
		pub2, err2 := p.getPublicKey(context.Background(), true)
		if err2 == nil && pub2 != nil && pub2.N.Cmp(pub.N) != 0 {
			if err := rsa.VerifyPKCS1v15(pub2, crypto.SHA512, hash[:], sig); err == nil {
				return nil
			}
		}
		return &provider.ProviderError{
			Provider: p.Name(),
			Code:     "signature_mismatch",
			Message:  "wata webhook signature verification failed",
			Err:      err,
		}
	}
	return nil
}

// getPublicKey возвращает RSA public key WATA, кешируя на publicKeyTTL.
// force=true игнорирует кэш и запрашивает свежий ключ (для случая ротации).
func (p *WataProvider) getPublicKey(ctx context.Context, force bool) (*rsa.PublicKey, error) {
	if !force {
		p.mu.RLock()
		if p.publicKey != nil && time.Since(p.publicKeyAt) < publicKeyTTL {
			pub := p.publicKey
			p.mu.RUnlock()
			return pub, nil
		}
		p.mu.RUnlock()
	}

	pub, err := p.fetchPublicKey(ctx)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.publicKey = pub
	p.publicKeyAt = time.Now()
	p.mu.Unlock()
	return pub, nil
}

// fetchPublicKey запрашивает GET /public-key и парсит PEM.
//
// WATA возвращает ключ в PKCS1 (`-----BEGIN RSA PUBLIC KEY-----`) согласно их
// доке, но мы поддерживаем оба формата (PKIX/SPKI тоже) — на случай если они
// сменят формат, не упадём.
func (p *WataProvider) fetchPublicKey(ctx context.Context) (*rsa.PublicKey, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, "GET", p.baseURL+"/public-key", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.accessToken)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call wata public-key: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wata public-key status=%d body=%s", resp.StatusCode, string(body))
	}

	var pkResp struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &pkResp); err != nil {
		return nil, fmt.Errorf("unmarshal public-key response: %w", err)
	}
	if pkResp.Value == "" {
		return nil, errors.New("wata public-key response: empty value")
	}

	block, _ := pem.Decode([]byte(pkResp.Value))
	if block == nil {
		return nil, errors.New("wata public-key: failed to decode PEM")
	}

	// PKCS1 — основной формат WATA. Если не парсится — пробуем PKIX.
	if pub, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		p.logger.Info("wata public key fetched", zap.String("format", "PKCS1"))
		return pub, nil
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public-key (tried PKCS1 and PKIX): %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("wata public-key is not RSA: %T", pub)
	}
	p.logger.Info("wata public key fetched", zap.String("format", "PKIX"))
	return rsaPub, nil
}
