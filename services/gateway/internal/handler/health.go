package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// HealthHandler агрегирует readiness всех upstream gRPC-сервисов.
//
// Endpoint'ы:
//
//   - GET /live   — liveness. Всегда 200, пока процесс жив. Используется
//     контейнерным оркестратором (Docker/k8s) для рестарта мёртвых процессов.
//
//   - GET /ready  — readiness. Параллельно опрашивает grpc.health.v1.Health/Check
//     у всех апстримов с per-upstream таймаутом. 200 если все апстримы SERVING,
//     503 иначе. Используется внешним health-check'ом (UptimeRobot, CF LB) и
//     failover-скриптом.
//
//   - GET /health — alias для /ready. Сохранён для обратной совместимости.
type HealthHandler struct {
	logger  *zap.Logger
	clients map[string]grpc_health_v1.HealthClient
	timeout time.Duration
}

// NewHealthHandler принимает map имя_апстрима → *grpc.ClientConn.
// Имя используется в JSON-ответе и логах. Если ClientConn nil — апстрим
// пропускается (например, опциональный referral).
func NewHealthHandler(conns map[string]*grpc.ClientConn, logger *zap.Logger) *HealthHandler {
	clients := make(map[string]grpc_health_v1.HealthClient, len(conns))
	for name, c := range conns {
		if c == nil {
			continue
		}
		clients[name] = grpc_health_v1.NewHealthClient(c)
	}
	return &HealthHandler{
		logger:  logger,
		clients: clients,
		timeout: 2 * time.Second,
	}
}

// CheckResult — результат одной проверки апстрима.
type CheckResult struct {
	Status string `json:"status"`          // "ok", "down", "unknown"
	Error  string `json:"error,omitempty"` // текст ошибки, если есть
}

// HealthResponse — JSON-ответ /ready и /health.
type HealthResponse struct {
	Status     string                 `json:"status"`     // "ok", "degraded", "down"
	Service    string                 `json:"service"`    // "vpn-gateway"
	Checks     map[string]CheckResult `json:"checks"`     // по имени апстрима
	DurationMs int64                  `json:"duration_ms"`
}

// Live — liveness. Всегда 200, пока процесс жив.
func (h *HealthHandler) Live(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"service": "vpn-gateway",
	})
}

// Ready — readiness. Опрашивает все апстримы параллельно.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	checks := h.runAll(r.Context())

	allOK := true
	for _, c := range checks {
		if c.Status != "ok" {
			allOK = false
			break
		}
	}

	resp := HealthResponse{
		Service:    "vpn-gateway",
		Checks:     checks,
		DurationMs: time.Since(start).Milliseconds(),
	}
	if allOK {
		resp.Status = "ok"
	} else {
		resp.Status = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	if allOK {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// Health — alias для /ready (обратная совместимость).
func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	h.Ready(w, r)
}

// runAll параллельно проверяет все апстримы с таймаутом.
func (h *HealthHandler) runAll(parent context.Context) map[string]CheckResult {
	results := make(map[string]CheckResult, len(h.clients))
	if len(h.clients) == 0 {
		return results
	}

	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for name, cli := range h.clients {
		wg.Add(1)
		go func(name string, cli grpc_health_v1.HealthClient) {
			defer wg.Done()
			res := h.checkOne(parent, cli)
			mu.Lock()
			results[name] = res
			mu.Unlock()
		}(name, cli)
	}
	wg.Wait()
	return results
}

// checkOne делает gRPC Health.Check c таймаутом.
func (h *HealthHandler) checkOne(parent context.Context, cli grpc_health_v1.HealthClient) CheckResult {
	ctx, cancel := context.WithTimeout(parent, h.timeout)
	defer cancel()

	resp, err := cli.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		return CheckResult{Status: "down", Error: err.Error()}
	}
	switch resp.GetStatus() {
	case grpc_health_v1.HealthCheckResponse_SERVING:
		return CheckResult{Status: "ok"}
	case grpc_health_v1.HealthCheckResponse_NOT_SERVING:
		return CheckResult{Status: "down", Error: "NOT_SERVING"}
	default:
		return CheckResult{Status: "unknown", Error: resp.GetStatus().String()}
	}
}

// HealthCheck — старая функция, оставлена для обратной совместимости.
// Если HealthHandler не сконфигурирован, отдаём минимальный 200 OK.
// Deprecated: использовать HealthHandler.Live / .Ready / .Health.
func HealthCheck(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"service": "vpn-gateway",
	})
}
