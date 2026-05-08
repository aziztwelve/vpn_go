# 03. Health checks — реализация

**Дата:** 2026-05-03
**Статус:** ✅ Внедрено в проде
**Связан с:** [`00-ha-scaling-roadmap.md`](./00-ha-scaling-roadmap.md) → задача **0.6**

---

## 🎯 Что сделано

В Gateway появились три endpoint'а — `/live`, `/ready`, `/health` — с настоящей проверкой состояния всех апстримов и их БД. Это нужно:

- **Внешним health-check'ам** (UptimeRobot, Cloudflare LB) — чтобы триггерить failover.
- **Контейнерному оркестратору** (Docker, k8s) — чтобы рестартить мёртвый процесс.
- **Failover-скрипту** — чтобы знать «кто упал», по детальному JSON.

---

## 🏗 Архитектура

```
┌──────────────────────────────────────────────────────────────┐
│  GET /api/osmonai.com/{live,ready,health} (через Caddy)      │
└────────────────────┬─────────────────────────────────────────┘
                     │
                     ▼
┌──────────────────────────────────────────────────────────────┐
│  Gateway HealthHandler                                       │
│                                                              │
│  /live   → 200 OK всегда                                     │
│           (процесс жив = 200)                                │
│                                                              │
│  /ready  → опрашивает все апстримы параллельно через         │
│           grpc.health.v1.Health/Check                        │
│           timeout=2s на каждый, ответ за ~5–20мс             │
│           200 если все SERVING, 503 если хоть один down      │
│                                                              │
│  /health → alias /ready (BC, был и раньше)                   │
└────────────────────┬─────────────────────────────────────────┘
                     │ gRPC Health/Check (параллельно)
        ┌────────────┼────────────┬────────────┬─────────────┐
        ▼            ▼            ▼            ▼             ▼
 ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐
 │ auth-svc │ │ sub-svc  │ │ vpn-svc  │ │ pay-svc  │ │ ref-svc  │
 │          │ │          │ │          │ │          │ │          │
 │ Health{  │ │ Health{  │ │ Health{  │ │ Health{  │ │ Health{  │
 │  db.Ping │ │  db.Ping │ │  db.Ping │ │  db.Ping │ │  db.Ping │
 │ }        │ │ }        │ │ }        │ │ }        │ │ }        │
 └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘
      │            │            │            │            │
      └────────────┴────────────┴────────────┴────────────┘
                          │
                          ▼
                  ┌───────────────┐
                  │  vpn-postgres │
                  │  (общая БД)   │
                  └───────────────┘
```

**Семантика:**
- `/live` — «процесс жив». Не зависит от БД, апстримов, ничего. Всегда 200, пока Go-процесс отвечает на HTTP.
- `/ready` — «готов принимать боевой трафик». Если упал хотя бы один компонент (БД, любой gRPC-сервис) — 503.
- `/health` — устарело, но сохранено как alias `/ready` для обратной совместимости (внешние мониторы могут уже стучать туда).

---

## 📦 Компоненты

### 1. Платформа: `platform/pkg/grpc/health/health.go`

Реализует gRPC Health Checking Protocol v1 с **опциональными динамическими чеками**:

```go
// Минимум — статический SERVING:
health.RegisterService(grpcServer)

// С пингом БД — если БД не пингуется, статус NOT_SERVING:
health.RegisterServiceWithChecks(grpcServer, func(ctx context.Context) error {
    return db.Ping(ctx)
})
```

Внутри: каждый `CheckFunc` выполняется с таймаутом 1с (по умолчанию). Если хотя бы один вернул error — status = `NOT_SERVING`.

### 2. Upstream-сервисы (5 штук)

В каждом `internal/app/app.go` после `grpc.NewServer()`:

```go
grpchealth.RegisterServiceWithChecks(a.grpcServer, func(ctx context.Context) error {
    return a.db.Ping(ctx)
})
```

Сервисы:
- `auth-service` (`:50060`)
- `subscription-service` (`:50061`)
- `vpn-service` (`:50062`) — пингает только БД, не Xray pool (он gracefully degrading)
- `payment-service` (`:50063`)
- `referral-service` (`:50064`)

### 3. Gateway: `services/gateway/internal/handler/health.go`

```go
type HealthHandler struct {
    clients map[string]grpc_health_v1.HealthClient
    timeout time.Duration  // 2s
}

func (h *HealthHandler) Live(...)   // 200 OK
func (h *HealthHandler) Ready(...)  // опрос параллельно, агрегация
func (h *HealthHandler) Health(...) // alias .Ready
```

В Gateway `app.go` зарегистрировано:
```go
healthHandler := handler.NewHealthHandler(map[string]*grpc.ClientConn{
    "auth":         a.authClient.Conn(),
    "subscription": a.subscriptionClient.Conn(),
    "vpn":          a.vpnClient.Conn(),
    "payment":      a.paymentClient.Conn(),
    "referral":     referralConn(a.referralClient),  // nil-safe
}, a.logger)
router.Get("/live", healthHandler.Live)
router.Get("/ready", healthHandler.Ready)
router.Get("/health", healthHandler.Health)
```

### 4. Тесты: `services/gateway/internal/handler/health_test.go`

7 кейсов:
- `TestHealthHandler_Live_AlwaysOK` — `/live` всегда 200
- `TestHealthHandler_Ready_AllOK` — все SERVING → 200, status=ok
- `TestHealthHandler_Ready_OneDown` — один NOT_SERVING → 503, status=degraded, детализация в `checks`
- `TestHealthHandler_Ready_GRPCError` — connection refused → 503, error в JSON
- `TestHealthHandler_Ready_Timeout` — slow upstream таймаутится за 500ms, fast возвращается ok параллельно → 503
- `TestHealthHandler_Ready_Empty` — нет апстримов → 200 (пустой checks)
- `TestHealthHandler_Health_AliasReady` — `/health` ведёт себя как `/ready`

---

## 📊 Формат ответа

### `/live`
```json
{"service":"vpn-gateway","status":"ok"}
```
HTTP 200, не зависит ни от чего.

### `/ready` (всё ОК)
```json
{
  "status": "ok",
  "service": "vpn-gateway",
  "checks": {
    "auth":         {"status": "ok"},
    "payment":      {"status": "ok"},
    "subscription": {"status": "ok"},
    "vpn":          {"status": "ok"},
    "referral":     {"status": "ok"}
  },
  "duration_ms": 2
}
```
HTTP 200.

### `/ready` (БД упала)
```json
{
  "status": "degraded",
  "service": "vpn-gateway",
  "checks": {
    "auth":         {"status": "down", "error": "NOT_SERVING"},
    "payment":      {"status": "down", "error": "NOT_SERVING"},
    "subscription": {"status": "down", "error": "NOT_SERVING"},
    "vpn":          {"status": "down", "error": "NOT_SERVING"},
    "referral":     {"status": "down", "error": "NOT_SERVING"}
  },
  "duration_ms": 6
}
```
HTTP 503.

### `/ready` (процесс умер целиком)
```
TCP timeout / connection refused
```
HTTP 503/504 от вышестоящего proxy (Caddy/CF). Это и есть сигнал «весь backend down» для failover-скрипта.

---

## 🧪 Проверка failure-сценария на проде

Делал прямо сейчас — `docker stop vpn-postgres`:

```
=== /live (БД лежит) ===                       → 200 OK (процесс жив)
=== /ready (БД лежит) ===                      → 503, status=degraded, все 5 checks "down: NOT_SERVING", duration_ms=6
=== /ready (БД поднял через 5с) ===            → 200 OK, все ok, duration_ms=2
```

Реакция мгновенная: gRPC Health/Check мониторит `db.Ping()` в каждом сервисе, как только Postgres недоступен — статус сразу `NOT_SERVING`.

---

## 🚀 Использование

### Внешний health-check (UptimeRobot, CF LB)

Мониторить `https://api.osmonai.com/ready` → `keyword: "ok"` или просто HTTP 2xx.

При 503 / timeout / DNS-fail → триггер failover (см. [`00-ha-scaling-roadmap.md`](./00-ha-scaling-roadmap.md) задача 1.7).

### Docker healthcheck (опционально, можно добавить в compose)

В `deploy/compose/docker-compose.yml` к сервису gateway:

```yaml
gateway:
  # ...
  healthcheck:
    test: ["CMD", "wget", "-qO-", "http://localhost:8081/live"]
    interval: 30s
    timeout: 5s
    retries: 3
    start_period: 10s
```

Использовать `/live`, не `/ready`! `/ready` зависит от внешних апстримов; если БД мигрирует — gateway не должен рестартиться, он не виноват.

### k8s probes (если когда-нибудь — но мы решили НЕ k8s)

```yaml
livenessProbe:  { httpGet: { path: /live,  port: 8081 } }
readinessProbe: { httpGet: { path: /ready, port: 8081 } }
```

### Failover-скрипт

```bash
# В failover.sh:
RESPONSE=$(curl -sf --max-time 10 https://api.osmonai.com/ready)
if [ $? -ne 0 ]; then
    # Backend полностью down — переключаем DNS на mirror
    trigger_dns_failover
fi
```

---

## ⚙️ Принятые компромиссы

1. **Vpn pool в /ready не пингаем.** Multi-server Xray делает best-effort partial success — падение одного exit-а не должно валить readiness всего бэкенда. Если все exit-ы упадут — это уже видно через `vpn_servers.is_active` и алертится отдельно.

2. **Кэширование чеков нет.** Каждый запрос /ready делает gRPC к 5 сервисам. На скейле это незначительная нагрузка (5 вызовов × несколько раз в минуту = ничто), но если health-check будет долбить, скажем, раз в секунду — стоит закэшировать на 1–2 секунды.

3. **/health = /ready (alias).** Можно было бы оставить /health «легче» (только наличие процесса), но это путает ожидания. Сейчас /health честно говорит «готов или нет». Любые мониторы которые стучали в /health раньше — продолжают работать, только теперь возвращают 503 при реальной проблеме.

4. **Параллельный опрос с агрегацией ВСЕХ.** Не используется fail-fast (вернуть 503 при первом down) — наоборот, дожидаемся всех, чтобы JSON был полным. Это нужно операторам для диагностики «что именно умерло».

5. **/ready не различает «warmup» и «runtime».** На старте gateway сразу READY (если апстримы отвечают). Можно было бы добавить «degrading» состояние, но в нашем масштабе оверкилл.

---

## 📋 Что осталось за бортом (не сейчас)

- **Метрики Prometheus** для каждого чека (latency, success rate). Когда подключим Prometheus (задача 0.2 в roadmap'е) — добавим экспортёр.
- **Watch RPC** реализован минимально (push один раз). Для гипер-low-latency failover можно реализовать настоящий streaming, но в http-агрегаторе это всё равно не используется.
- **Прометей-style /metrics**. Отдельная задача.
- **Кеширование** результатов чеков — добавим если health-check'и будут стучать чаще раза в секунду.

---

## 🗺 Где это в коде

| Файл | Что |
|---|---|
| [`platform/pkg/grpc/health/health.go`](../../platform/pkg/grpc/health/health.go) | Health-сервер с CheckFunc-поддержкой |
| [`services/auth-service/internal/app/app.go`](../../services/auth-service/internal/app/app.go) | `RegisterServiceWithChecks(db.Ping)` |
| [`services/payment-service/internal/app/app.go`](../../services/payment-service/internal/app/app.go) | то же |
| [`services/subscription-service/internal/app/app.go`](../../services/subscription-service/internal/app/app.go) | то же |
| [`services/vpn-service/internal/app/app.go`](../../services/vpn-service/internal/app/app.go) | то же (без Xray pool — комментарий внутри) |
| [`services/referral-service/internal/app/app.go`](../../services/referral-service/internal/app/app.go) | то же |
| [`services/gateway/internal/handler/health.go`](../../services/gateway/internal/handler/health.go) | `HealthHandler` — Live/Ready/Health |
| [`services/gateway/internal/handler/health_test.go`](../../services/gateway/internal/handler/health_test.go) | Unit-тесты (7 кейсов) |
| [`services/gateway/internal/app/app.go`](../../services/gateway/internal/app/app.go) | Регистрация роутов `/live`, `/ready`, `/health` |
| [`services/gateway/internal/client/*.go`](../../services/gateway/internal/client/) | Геттеры `Conn()` для всех клиентов |
