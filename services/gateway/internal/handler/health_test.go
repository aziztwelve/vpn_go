package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// fakeHealthClient — мок grpc_health_v1.HealthClient для тестов.
type fakeHealthClient struct {
	status grpc_health_v1.HealthCheckResponse_ServingStatus
	err    error
	delay  time.Duration
}

func (f *fakeHealthClient) Check(ctx context.Context, _ *grpc_health_v1.HealthCheckRequest, _ ...grpc.CallOption) (*grpc_health_v1.HealthCheckResponse, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return &grpc_health_v1.HealthCheckResponse{Status: f.status}, nil
}

func (f *fakeHealthClient) Watch(_ context.Context, _ *grpc_health_v1.HealthCheckRequest, _ ...grpc.CallOption) (grpc_health_v1.Health_WatchClient, error) {
	return nil, errors.New("watch not implemented in fake")
}

// List — нужен для совместимости с интерфейсом grpc_health_v1.HealthClient (Health/List RPC).
func (f *fakeHealthClient) List(_ context.Context, _ *grpc_health_v1.HealthListRequest, _ ...grpc.CallOption) (*grpc_health_v1.HealthListResponse, error) {
	return nil, errors.New("list not implemented in fake")
}

func newHealthHandlerWithFakes(fakes map[string]*fakeHealthClient) *HealthHandler {
	clients := make(map[string]grpc_health_v1.HealthClient, len(fakes))
	for name, f := range fakes {
		clients[name] = f
	}
	return &HealthHandler{
		logger:  zap.NewNop(),
		clients: clients,
		timeout: 500 * time.Millisecond,
	}
}

func TestHealthHandler_Live_AlwaysOK(t *testing.T) {
	h := &HealthHandler{logger: zap.NewNop(), timeout: time.Second}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/live", nil)
	h.Live(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Live: expected 200, got %d", rec.Code)
	}
}

func TestHealthHandler_Ready_AllOK(t *testing.T) {
	h := newHealthHandlerWithFakes(map[string]*fakeHealthClient{
		"auth":    {status: grpc_health_v1.HealthCheckResponse_SERVING},
		"vpn":     {status: grpc_health_v1.HealthCheckResponse_SERVING},
		"payment": {status: grpc_health_v1.HealthCheckResponse_SERVING},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	h.Ready(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Ready all-ok: expected 200, got %d", rec.Code)
	}
	var resp HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status=ok, got %s", resp.Status)
	}
	if len(resp.Checks) != 3 {
		t.Errorf("expected 3 checks, got %d", len(resp.Checks))
	}
	for name, c := range resp.Checks {
		if c.Status != "ok" {
			t.Errorf("check %s expected ok, got %s (err: %s)", name, c.Status, c.Error)
		}
	}
}

func TestHealthHandler_Ready_OneDown(t *testing.T) {
	h := newHealthHandlerWithFakes(map[string]*fakeHealthClient{
		"auth":    {status: grpc_health_v1.HealthCheckResponse_SERVING},
		"vpn":     {status: grpc_health_v1.HealthCheckResponse_NOT_SERVING},
		"payment": {status: grpc_health_v1.HealthCheckResponse_SERVING},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	h.Ready(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Ready one-down: expected 503, got %d", rec.Code)
	}
	var resp HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "degraded" {
		t.Errorf("expected status=degraded, got %s", resp.Status)
	}
	if resp.Checks["vpn"].Status != "down" {
		t.Errorf("vpn expected down, got %s", resp.Checks["vpn"].Status)
	}
	if resp.Checks["auth"].Status != "ok" {
		t.Errorf("auth expected ok, got %s", resp.Checks["auth"].Status)
	}
}

func TestHealthHandler_Ready_GRPCError(t *testing.T) {
	h := newHealthHandlerWithFakes(map[string]*fakeHealthClient{
		"auth": {err: errors.New("connection refused")},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	h.Ready(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Ready grpc-error: expected 503, got %d", rec.Code)
	}
	var resp HealthResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Checks["auth"].Status != "down" {
		t.Errorf("auth expected down, got %s", resp.Checks["auth"].Status)
	}
	if resp.Checks["auth"].Error == "" {
		t.Error("auth expected error message")
	}
}

func TestHealthHandler_Ready_Timeout(t *testing.T) {
	h := newHealthHandlerWithFakes(map[string]*fakeHealthClient{
		"slow": {delay: 2 * time.Second, status: grpc_health_v1.HealthCheckResponse_SERVING},
		"fast": {status: grpc_health_v1.HealthCheckResponse_SERVING},
	})
	// timeout у handler = 500ms, slow = 2s → должен таймаутнуться

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	h.Ready(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Ready timeout: expected 503, got %d", rec.Code)
	}
	var resp HealthResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Checks["slow"].Status != "down" {
		t.Errorf("slow expected down (timeout), got %s", resp.Checks["slow"].Status)
	}
	if resp.Checks["fast"].Status != "ok" {
		t.Errorf("fast expected ok (parallel), got %s", resp.Checks["fast"].Status)
	}
}

func TestHealthHandler_Ready_Empty(t *testing.T) {
	// Если апстримов нет (nil ClientConn'ы отфильтрованы) — отдаём ok.
	h := &HealthHandler{
		logger:  zap.NewNop(),
		clients: map[string]grpc_health_v1.HealthClient{},
		timeout: time.Second,
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	h.Ready(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Ready empty: expected 200, got %d", rec.Code)
	}
	var resp HealthResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Status != "ok" {
		t.Errorf("empty ready expected ok, got %s", resp.Status)
	}
}

func TestHealthHandler_Health_AliasReady(t *testing.T) {
	h := newHealthHandlerWithFakes(map[string]*fakeHealthClient{
		"auth": {status: grpc_health_v1.HealthCheckResponse_SERVING},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.Health(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/health alias: expected 200, got %d", rec.Code)
	}
}
