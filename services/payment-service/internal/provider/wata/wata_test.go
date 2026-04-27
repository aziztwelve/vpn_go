package wata

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func sign(t *testing.T, key *rsa.PrivateKey, payload []byte) string {
	t.Helper()
	hash := sha512.Sum512(payload)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA512, hash[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func pubPEMPKCS1(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	block := &pem.Block{
		Type:  "RSA PUBLIC KEY",
		Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey),
	}
	return string(pem.EncodeToMemory(block))
}

func pubPEMPKIX(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func fakeServer(t *testing.T, pemKey string, hits *int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/public-key", func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"value": pemKey})
	})
	return httptest.NewServer(mux)
}

func newTestProvider(t *testing.T, baseURL string) *WataProvider {
	t.Helper()
	logger, _ := zap.NewDevelopment()
	return NewProvider(Config{
		BaseURL:     baseURL,
		AccessToken: "test-token",
		SuccessURL:  "https://example.com/success",
		FailURL:     "https://example.com/fail",
		LinkTTL:     72 * time.Hour,
		Logger:      logger,
	})
}

func TestValidateWebhook_Valid_PKCS1(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	srv := fakeServer(t, pubPEMPKCS1(t, key), nil)
	defer srv.Close()

	p := newTestProvider(t, srv.URL)
	payload := []byte(`{"orderId":"payment_42","status":"Paid"}`)
	if err := p.ValidateWebhook(payload, sign(t, key, payload)); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestValidateWebhook_Valid_PKIX(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := fakeServer(t, pubPEMPKIX(t, key), nil)
	defer srv.Close()

	p := newTestProvider(t, srv.URL)
	payload := []byte(`hello`)
	if err := p.ValidateWebhook(payload, sign(t, key, payload)); err != nil {
		t.Fatalf("expected valid for PKIX, got: %v", err)
	}
}

func TestValidateWebhook_BadSignature(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := fakeServer(t, pubPEMPKCS1(t, key), nil)
	defer srv.Close()
	p := newTestProvider(t, srv.URL)

	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	err := p.ValidateWebhook([]byte(`payload`), sign(t, otherKey, []byte(`payload`)))
	if err == nil {
		t.Fatal("expected signature_mismatch, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "signature") {
		t.Fatalf("expected signature error, got: %v", err)
	}
}

func TestValidateWebhook_TamperedPayload(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := fakeServer(t, pubPEMPKCS1(t, key), nil)
	defer srv.Close()
	p := newTestProvider(t, srv.URL)

	// Подписываем один payload, но отдаём другой — подпись не должна совпасть.
	sig := sign(t, key, []byte(`original`))
	if err := p.ValidateWebhook([]byte(`tampered`), sig); err == nil {
		t.Fatal("expected error for tampered payload")
	}
}

func TestValidateWebhook_EmptySignature(t *testing.T) {
	p := newTestProvider(t, "http://nonexistent")
	if err := p.ValidateWebhook([]byte(`x`), ""); err == nil {
		t.Fatal("expected error for empty signature")
	}
}

func TestValidateWebhook_BadBase64(t *testing.T) {
	p := newTestProvider(t, "http://nonexistent")
	if err := p.ValidateWebhook([]byte(`x`), "!!!not-base64!!!"); err == nil {
		t.Fatal("expected error for non-base64 signature")
	}
}

func TestPublicKeyCache(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	hits := 0
	srv := fakeServer(t, pubPEMPKCS1(t, key), &hits)
	defer srv.Close()

	p := newTestProvider(t, srv.URL)
	payload := []byte(`abc`)
	sig := sign(t, key, payload)

	for i := 0; i < 3; i++ {
		if err := p.ValidateWebhook(payload, sig); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	if hits != 1 {
		t.Fatalf("expected 1 public-key fetch (cached), got %d", hits)
	}

	if _, err := p.getPublicKey(context.Background(), true); err != nil {
		t.Fatalf("force refresh: %v", err)
	}
	if hits != 2 {
		t.Fatalf("expected 2 fetches after force refresh, got %d", hits)
	}
}
