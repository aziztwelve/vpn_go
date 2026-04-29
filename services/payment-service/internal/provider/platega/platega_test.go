package platega

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vpn/payment-service/internal/provider"
	"go.uber.org/zap"
)

const (
	testMerchantID = "11111111-2222-3333-4444-555555555555"
	testAPISecret  = "super-secret-api-key"
)

func newTestProvider(baseURL string, defaultMethod int) *PlategaProvider {
	return NewProvider(Config{
		BaseURL:       baseURL,
		MerchantID:    testMerchantID,
		APISecret:     testAPISecret,
		SuccessURL:    "https://example.com/success",
		FailURL:       "https://example.com/fail",
		DefaultMethod: defaultMethod,
		Logger:        zap.NewNop(),
	})
}

// ─── ValidateWebhook ─────────────────────────────────────────────────────────

func TestValidateWebhook_OK(t *testing.T) {
	p := newTestProvider("https://example.com", 0)
	if err := p.ValidateWebhook(nil, testMerchantID+":"+testAPISecret); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestValidateWebhook_BadMerchant(t *testing.T) {
	p := newTestProvider("https://example.com", 0)
	err := p.ValidateWebhook(nil, "wrong-merchant-id:"+testAPISecret)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	pe, ok := err.(*provider.ProviderError)
	if !ok || pe.Code != "invalid_credentials" {
		t.Fatalf("expected invalid_credentials, got %v", err)
	}
}

func TestValidateWebhook_BadSecret(t *testing.T) {
	p := newTestProvider("https://example.com", 0)
	err := p.ValidateWebhook(nil, testMerchantID+":wrong-secret")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	pe, ok := err.(*provider.ProviderError)
	if !ok || pe.Code != "invalid_credentials" {
		t.Fatalf("expected invalid_credentials, got %v", err)
	}
}

func TestValidateWebhook_Empty(t *testing.T) {
	p := newTestProvider("https://example.com", 0)
	err := p.ValidateWebhook(nil, "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	pe := err.(*provider.ProviderError)
	if pe.Code != "missing_credentials" {
		t.Fatalf("expected missing_credentials, got %s", pe.Code)
	}
}

func TestValidateWebhook_NoColon(t *testing.T) {
	p := newTestProvider("https://example.com", 0)
	err := p.ValidateWebhook(nil, "no-colon-here")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	pe := err.(*provider.ProviderError)
	if pe.Code != "invalid_credentials_format" {
		t.Fatalf("expected invalid_credentials_format, got %s", pe.Code)
	}
}

// ─── HandleWebhook ───────────────────────────────────────────────────────────

func TestHandleWebhook_Confirmed(t *testing.T) {
	p := newTestProvider("https://example.com", 0)
	body := mustJSON(t, callbackPayload{
		ID:            "tx-uuid-1",
		Amount:        499.00,
		Currency:      "RUB",
		Status:        "CONFIRMED",
		PaymentMethod: 2,
		Payload:       "payment_111_2_3_1700000000",
	})

	ev, err := p.HandleWebhook(context.Background(), body, testMerchantID+":"+testAPISecret)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ev.Status != "paid" {
		t.Errorf("status: want paid, got %s", ev.Status)
	}
	if ev.UserID != 111 || ev.PlanID != 2 || ev.MaxDevices != 3 {
		t.Errorf("ids: got user=%d plan=%d dev=%d", ev.UserID, ev.PlanID, ev.MaxDevices)
	}
	if ev.ExternalID != "tx-uuid-1" {
		t.Errorf("external_id: %s", ev.ExternalID)
	}
}

func TestHandleWebhook_StatusMapping(t *testing.T) {
	p := newTestProvider("https://example.com", 0)
	cases := map[string]string{
		"CONFIRMED":    "paid",
		"CANCELED":     "cancelled",
		"CANCELLED":    "cancelled", // допускаем оба варианта
		"CHARGEBACKED": "refunded",
		"PENDING":      "pending",
		"WHATEVER":     "pending", // unknown → pending
	}
	for raw, want := range cases {
		body := mustJSON(t, callbackPayload{
			ID:      "tx",
			Status:  raw,
			Payload: "payment_1_2_3_4",
		})
		ev, err := p.HandleWebhook(context.Background(), body, testMerchantID+":"+testAPISecret)
		if err != nil {
			t.Errorf("status %s: unexpected error: %v", raw, err)
			continue
		}
		if ev.Status != want {
			t.Errorf("status %s: want %s got %s", raw, want, ev.Status)
		}
	}
}

func TestHandleWebhook_BadCreds(t *testing.T) {
	p := newTestProvider("https://example.com", 0)
	body := mustJSON(t, callbackPayload{ID: "tx", Status: "CONFIRMED", Payload: "payment_1_2_3_4"})
	_, err := p.HandleWebhook(context.Background(), body, "wrong:wrong")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestHandleWebhook_BadPayloadField(t *testing.T) {
	p := newTestProvider("https://example.com", 0)
	body := mustJSON(t, callbackPayload{ID: "tx", Status: "CONFIRMED", Payload: "garbage"})
	_, err := p.HandleWebhook(context.Background(), body, testMerchantID+":"+testAPISecret)
	if err == nil {
		t.Fatalf("expected error")
	}
	pe := err.(*provider.ProviderError)
	if pe.Code != "invalid_payload_field" {
		t.Errorf("expected invalid_payload_field, got %s", pe.Code)
	}
}

func TestHandleWebhook_BadJSON(t *testing.T) {
	p := newTestProvider("https://example.com", 0)
	_, err := p.HandleWebhook(context.Background(), []byte("not-json"), testMerchantID+":"+testAPISecret)
	if err == nil {
		t.Fatalf("expected error")
	}
	pe := err.(*provider.ProviderError)
	if pe.Code != "invalid_payload" {
		t.Errorf("expected invalid_payload, got %s", pe.Code)
	}
}

// ─── CreateInvoice ───────────────────────────────────────────────────────────

func TestCreateInvoice_V2(t *testing.T) {
	var (
		gotPath    string
		gotMID     string
		gotSecret  string
		gotBodyRaw []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMID = r.Header.Get("X-MerchantId")
		gotSecret = r.Header.Get("X-Secret")
		gotBodyRaw, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"transactionId": "tx-uuid-1",
			"status":        "PENDING",
			"url":           "https://pay.platega.io/?id=foo",
			"expiresIn":     "00:15:00",
		})
	}))
	defer srv.Close()

	p := newTestProvider(srv.URL, 0) // v2
	inv, err := p.CreateInvoice(context.Background(), &provider.CreateInvoiceRequest{
		UserID: 111, PlanID: 2, MaxDevices: 3,
		AmountRUB: 499.0, Description: "VPN test",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if inv.ExternalID != "tx-uuid-1" {
		t.Errorf("external_id: %s", inv.ExternalID)
	}
	if inv.InvoiceLink != "https://pay.platega.io/?id=foo" {
		t.Errorf("link: %s", inv.InvoiceLink)
	}
	if gotPath != "/v2/transaction/process" {
		t.Errorf("path: want /v2/transaction/process, got %s", gotPath)
	}
	if gotMID != testMerchantID || gotSecret != testAPISecret {
		t.Errorf("creds: mid=%s sec=%s", gotMID, gotSecret)
	}
	// body should NOT contain paymentMethod
	if strings.Contains(string(gotBodyRaw), "paymentMethod") {
		t.Errorf("v2 body must not contain paymentMethod, got: %s", string(gotBodyRaw))
	}
	// body должен содержать наш payload
	var parsed map[string]any
	_ = json.Unmarshal(gotBodyRaw, &parsed)
	pl, _ := parsed["payload"].(string)
	if !strings.HasPrefix(pl, "payment_111_2_3_") {
		t.Errorf("payload field wrong: %s", pl)
	}
}

func TestCreateInvoice_V1WithMethod(t *testing.T) {
	var gotPath string
	var gotBodyRaw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBodyRaw, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"transactionId": "tx-uuid-2",
			"status":        "PENDING",
			"redirect":      "https://pay.platega.io/?qrsbp",
			"expiresIn":     "00:15:00",
		})
	}))
	defer srv.Close()

	p := newTestProvider(srv.URL, 2) // v1 with СБП QR
	inv, err := p.CreateInvoice(context.Background(), &provider.CreateInvoiceRequest{
		UserID: 1, PlanID: 1, MaxDevices: 1, AmountRUB: 100, Description: "x",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if gotPath != "/transaction/process" {
		t.Errorf("path: want /transaction/process, got %s", gotPath)
	}
	if !strings.Contains(string(gotBodyRaw), `"paymentMethod":2`) {
		t.Errorf("v1 body must contain paymentMethod=2, got: %s", string(gotBodyRaw))
	}
	if inv.InvoiceLink != "https://pay.platega.io/?qrsbp" {
		t.Errorf("link should fallback to redirect: %s", inv.InvoiceLink)
	}
}

func TestCreateInvoice_BadAmount(t *testing.T) {
	p := newTestProvider("http://does-not-matter", 0)
	_, err := p.CreateInvoice(context.Background(), &provider.CreateInvoiceRequest{
		UserID: 1, PlanID: 1, MaxDevices: 1, AmountRUB: 0,
	})
	if err == nil {
		t.Fatalf("expected error for zero amount")
	}
	pe := err.(*provider.ProviderError)
	if pe.Code != "invalid_amount" {
		t.Errorf("expected invalid_amount, got %s", pe.Code)
	}
}

func TestCreateInvoice_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad merchant"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := newTestProvider(srv.URL, 0)
	_, err := p.CreateInvoice(context.Background(), &provider.CreateInvoiceRequest{
		UserID: 1, PlanID: 1, MaxDevices: 1, AmountRUB: 100,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	pe := err.(*provider.ProviderError)
	if pe.Code != "api_error" {
		t.Errorf("expected api_error, got %s", pe.Code)
	}
}

func TestCreateInvoice_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Возвращаем валидный JSON но без transactionId
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := newTestProvider(srv.URL, 0)
	_, err := p.CreateInvoice(context.Background(), &provider.CreateInvoiceRequest{
		UserID: 1, PlanID: 1, MaxDevices: 1, AmountRUB: 100,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	pe := err.(*provider.ProviderError)
	if pe.Code != "invalid_response" {
		t.Errorf("expected invalid_response, got %s", pe.Code)
	}
}

// ─── helpers ────────────────────────────────────────────────────────────────

func TestParsePayload(t *testing.T) {
	cases := []struct {
		in       string
		ok       bool
		uid      int64
		plan     int32
		dev      int32
	}{
		{"payment_111_2_3_1700000000", true, 111, 2, 3},
		{"payment_1_1_1_0", true, 1, 1, 1},
		{"payment_111_2_3", false, 0, 0, 0},                // мало сегментов
		{"payment_111_2_3_4_5", false, 0, 0, 0},            // много сегментов
		{"order_111_2_3_4", false, 0, 0, 0},                // не наш префикс
		{"payment_abc_2_3_4", false, 0, 0, 0},              // нечисловой userID
		{"payment_111_xx_3_4", false, 0, 0, 0},             // нечисловой planID
	}
	for _, c := range cases {
		uid, plan, dev, err := parsePayload(c.in)
		if c.ok {
			if err != nil {
				t.Errorf("%q: unexpected error %v", c.in, err)
				continue
			}
			if uid != c.uid || plan != c.plan || dev != c.dev {
				t.Errorf("%q: got uid=%d plan=%d dev=%d", c.in, uid, plan, dev)
			}
		} else {
			if err == nil {
				t.Errorf("%q: expected error", c.in)
			}
		}
	}
}

func TestParseHHMMSS(t *testing.T) {
	cases := map[string]bool{
		"00:15:00":  true,
		"01:00:00":  true,
		"00:00:30":  true,
		"15:00":     false,
		"abc:de:fg": false,
		"":          false,
	}
	for s, ok := range cases {
		_, got := parseHHMMSS(s)
		if got != ok {
			t.Errorf("%q: want %v got %v", s, ok, got)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
