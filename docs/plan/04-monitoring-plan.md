# 04. Мониторинг — план

**Дата:** 2026-05-03
**Статус:** 📝 План готов к реализации (хост — `fi02`, транспорт — WireGuard)
**Связан с:** [`00-ha-scaling-roadmap.md`](./00-ha-scaling-roadmap.md) → задачи **0.2** (Prometheus + Grafana + Telegram alerts), **0.3** (Loki/Vector логи)
**Автор:** Devin + Aziz

---

## 📌 Краткое резюме решений

| Вопрос | Решение |
|---|---|
| Где хостить стек | `fi02` (Hetzner Helsinki, 4 GB RAM, 2 vCPU, Xray exit-нода) |
| Транспорт `/metrics` | WireGuard mesh между backend ↔ fi02 |
| Доступ к Grafana | Публично `grafana.osmonai.com` + сильный admin-пароль (позже — OAuth/2FA) |
| Альтернативы которые отклонили | Datadog ($75/мес), Sentry (не нужен при Loki), OpenTelemetry traces (пока нет triggers) |
| Стоимость | $0 сейчас, $1-2/мес если добавим S3 для логов |
| Кода писать | ~300-400 строк Go: `platform/pkg/metrics` + 2 строки в каждом из 6 сервисов + точечные бизнес-метрики |

---

## 🎯 Зачем

Сейчас (см. [`01-current-state-audit.md`](./01-current-state-audit.md)):
- ✅ Логи есть (zap, JSON, в `docker logs`)
- ❌ Нет агрегации логов (нельзя `grep` по 5 контейнерам сразу)
- ❌ Нет метрик (только `docker stats` руками)
- ❌ Нет alerting'а (узнаём о проблеме от юзера через час-два)
- ❌ Нет дашбордов (всё через `psql` и `docker logs`)
- ❌ В go.mod нет ни `prometheus`, ни `otel`, ни `sentry`

**Цель плана:**
1. Узнавать о проблеме **раньше юзера** (за 1-2 минуты, не за час).
2. Иметь данные для **разбора инцидента** задним числом (что было, кто упал первым).
3. Видеть **бизнес-метрики**: регистрации/час, конверсия в платящих, активные туннели — а не только «CPU 60%».
4. Дать **Aziz'у** дашборд который можно открыть на телефоне и за 30 секунд понять «всё ОК / есть проблема».

---

## 🧱 Категории мониторинга (4 уровня)

Это важно разделить, чтобы не свалить всё в кучу. Разные категории — разные инструменты и разные алерты.

### A. Infrastructure (host)
Здоровье железа и базовых сервисов. Это «ниже» уровня приложения.
- CPU, RAM, disk, network на VPS
- Postgres: connections, slow queries, locks, replication lag (когда будет replica)
- Docker: контейнер up/down, restart count, OOM-kill события
- TLS-сертификаты (срок до истечения)

### B. Service health (RED + USE)
RED-метод (Rate, Errors, Duration) — **снаружи** к сервису. USE-метод (Utilization, Saturation, Errors) — **внутри** ресурсов сервиса.
- HTTP RED по эндпоинтам Gateway
- gRPC RED для каждого upstream (auth/sub/vpn/payment/referral)
- БД-пул USE (busy/idle connections, wait time)
- xray.Pool USE (state по каждому серверу, latency запросов)

### C. Business metrics
Это самое важное: метрики которые **прямо** говорят «бизнес работает». Технические метрики могут быть зелёные, а бизнес — стоять.
- Регистрации/час, /день
- Конверсия `pending → paid` по каждому провайдеру
- Активные подписки, истекающие в 24ч
- Bandwidth по серверам, активные туннели
- Sentinel — сколько застрявших платежей резолвит
- ExpireCron — сколько истёкших обработал

### D. External / synthetic
Проверки **снаружи** инфры. Если домен умер — изнутри ты этого не увидишь.
- Probe `https://api.osmonai.com/live` из 2-3 географий
- TLS expiration (Caddy)
- DNS правильность
- TCP-доступность Xray exit-нод (порт 8443)

---

## 🛠 Стек: что выбрать и почему

### Рекомендация: классический Prometheus stack

```
┌─────────────────────────────────────────────────────────────┐
│  Backend VPS (или fi02 — см. ниже)                          │
│                                                             │
│  ┌──────────────┐  scrapes  ┌────────────────────────┐      │
│  │ Prometheus   │◄──────────┤ /metrics endpoints     │      │
│  │ (TSDB, 15d)  │           │ - gateway              │      │
│  └──────┬───────┘           │ - auth/sub/vpn/pay/ref │      │
│         │                   │ - node_exporter        │      │
│         │                   │ - postgres_exporter    │      │
│         │                   │ - cadvisor             │      │
│         │                   │ - blackbox_exporter    │      │
│         │ remote_read       └────────────────────────┘      │
│         ▼                                                   │
│  ┌──────────────┐                                           │
│  │ Grafana      │  ← Aziz открывает на телефоне            │
│  │ (dashboards) │                                           │
│  └──────────────┘                                           │
│                                                             │
│  ┌──────────────┐  alerts   ┌────────────────────────┐      │
│  │ Alertmanager │──────────►│ Telegram bot           │      │
│  │              │           │ → @maydavpn_alerts     │      │
│  └──────────────┘           └────────────────────────┘      │
│                                                             │
│  ┌──────────────┐  pulls    ┌────────────────────────┐      │
│  │ Loki         │◄──────────┤ Promtail               │      │
│  │ (logs, 14d)  │           │ (читает docker logs)   │      │
│  └──────┬───────┘           └────────────────────────┘      │
│         └─────► Grafana (Logs Explore + Alerts)             │
└─────────────────────────────────────────────────────────────┘
```

### Что и почему

| Компонент | Зачем | Альтернатива | Почему именно это |
|---|---|---|---|
| **Prometheus** | TSDB для метрик, scraping | VictoriaMetrics | Стандарт, экосистема, всё совместимо. VM лучше при сотнях GB — у нас не тот объём. |
| **Grafana** | Дашборды + Alerts UI | Perses, Datadog | Стандарт, лучшая визуализация, бесплатно. |
| **Alertmanager** | Маршрутизация и группировка алертов | Grafana Alerting | AM лучше для зрелого setup'а; Grafana Alerting может встать как 2-я фаза. На MVP — Grafana Alerting (проще, единый UI). |
| **node_exporter** | Host-метрики (CPU, RAM, диск) | telegraf | Стандарт, точечно для Prometheus. |
| **postgres_exporter** | Postgres-метрики | pgwatch2 | Стандарт, поддерживает кастомные queries для бизнес-метрик. |
| **cadvisor** | Per-container CPU/RAM/network | docker_exporter | cadvisor от Google, точнее. |
| **blackbox_exporter** | Synthetic probes (HTTP, TCP, DNS) | uptime-kuma | Лучше интегрируется с Prometheus, можно гонять из 2-3 географий. |
| **Loki** | Лог-агрегация | Elasticsearch (ELK) | Loki в 10 раз дешевле по дискам и проще в эксплуатации. ELK — это 4 GB RAM минимум, нам ни к чему. |
| **Promtail** | Читает Docker logs → Loki | Vector, fluent-bit | Идеально совместим с Loki, минимальный конфиг. |

### Что НЕ брать (и почему)

- **OpenTelemetry / Jaeger / Tempo (traces).** Хорошо для multi-microservice latency дебага, но в нашем масштабе (5 сервисов, 50 RPS пиково) — оверкилл. Метрики + структурированные логи решают 90% задач. **Добавим если** появятся «тут где-то висит, не понимаю где» проблемы которые нельзя разобрать логами.
- **Sentry / Bugsnag.** Полезно для прод-ошибок с stack trace. Альтернатива: Loki-алерты на `level=ERROR` rate. Если решим — Sentry SaaS бесплатный до 5к ошибок/мес.
- **VictoriaMetrics вместо Prometheus.** Лучше при объёме. У нас на 10к юзеров будет ~1 GB метрик, P справится.
- **Datadog / New Relic / Better Stack.** $$$. Datadog считает $15/host/мес × 5 контейнеров = $75/мес ≈ 7к₽/мес. Тот же стек self-hosted = $0–5.
- **Uptime Kuma как замена всего.** Хорошее для status-page, но не для метрик.
- **Statsd + Graphite.** Устарело, Prometheus лучше во всём.

---

## 🌐 Где хостить

Два варианта:

### Вариант A: На самом backend VPS
Все компоненты в том же docker-compose что бэкенд. Просто, быстро.

**За:** ничего не настраивать сетево, +500MB RAM нагрузка на 8GB VPS — не критично.
**Против:** при падении backend'а монитор тоже умрёт. Алерт `https://api.osmonai.com down` придёт **снаружи** (UptimeRobot бесплатный), но детальной картины «почему упал» уже не увидишь — Grafana мертва.

### Вариант B: На отдельном VPS (выбран) — `fi02` (Hetzner Helsinki)
Prometheus + Grafana + Loki + Alertmanager на `fi02` (Xray exit-нода в Финляндии, есть SSH, гео-распределённо относительно backend в Германии).

**За:**
- Падение backend'а / ДЦ Германии не убивает мониторинг.
- Гео-распределённый probe (мониторим backend из FI — реальный пользовательский опыт).
- Можем мониторить вообще всё (backend + nl01 + fi02) централизованно.
- Ресурсов в избытке (см. ниже).

**Против:**
- Сетевая настройка — Prometheus должен ходить к `/metrics` через защищённый канал (WireGuard tunnel).

**Решение:** Вариант B, хост — `fi02`. Транспорт `/metrics` backend↔fi02 — через **WireGuard mesh** (пригодится также для будущей hot replica и failover).

#### Фактические ресурсы `fi02` (проверено 2026-05-03)

| Ресурс | Значение | Вердикт |
|---|---|---|
| Hardware | Hetzner Cloud, `ubuntu-4gb-hel1-1`, 2 vCPU AMD EPYC-Rome | ✅ |
| RAM | 3.7 GB total, **2.4 GB free** | ✅ мониторингу надо ~600 MB, запас 1.7 GB |
| Disk | 38 GB total, **20 GB free** (46% used) | ✅ Prometheus 15d + Loki 14d ≈ 10–15 GB |
| Swap | **0 B** | ⚠️ Добавить 1–2 GB swap как safety net (OOM insurance) |
| Load avg | 0.84 (из 2 vCPU = 42%) | ✅ |
| Uptime | 11 дней | ✅ |
| Работающие сервисы | `xray` up 3d | ✅ основное назначение ноды |

#### 🚨 Проблема обнаружена на `fi02`: `watchtower` в crash-loop
Контейнер `watchtower` "Restarting (1) About a minute ago". Watchtower = автоапдейтер Docker-контейнеров, каждый час обновляет образы из registry.

**Риск:** если он починится — может в рандомное время (даже ночью) подтянуть новую версию `xray` и перезапустить контейнер → 10–30 секунд downtime exit-ноды для всех юзеров на ней.

**Действие перед запуском мониторинга:**
- Разобраться почему watchtower падает (`docker logs watchtower`, вероятнее всего конфликт с новой версией Docker или сломанный конфиг).
- Либо **починить и ограничить scope** (только не-production контейнеры, или окно обновлений 03:00-04:00 UTC).
- Либо **полностью удалить** (`docker rm -f watchtower`) — обновления xray делаем руками при деплое.
- Рекомендую **удалить**: автоапдейты exit-ноды без supervision — это рискованно, одного revert'а upstream xray хватит чтобы уложить сервис.

---

## 📊 Что инструментировать в коде

### A. Новый общий пакет `platform/pkg/metrics`

Идиома: один пакет, который дёргают все сервисы. Должен предоставить:

1. **HTTP middleware** — метрики на каждый HTTP-запрос:
   - `http_requests_total{service, method, path, status}` — counter
   - `http_request_duration_seconds{service, method, path}` — histogram (бакеты: 5ms, 25ms, 100ms, 500ms, 2s, 10s)

2. **gRPC server interceptor** — метрики на каждый входящий gRPC:
   - `grpc_requests_total{service, method, code}` — counter
   - `grpc_request_duration_seconds{service, method}` — histogram

3. **gRPC client interceptor** — метрики на каждый исходящий gRPC:
   - `grpc_client_requests_total{service, target, method, code}` — counter
   - `grpc_client_request_duration_seconds{service, target, method}` — histogram

4. **`/metrics` HTTP endpoint** — экспорт всех метрик. На отдельном порту от боевого HTTP/gRPC (например, `:9090`+offset на каждом сервисе) чтобы не светить наружу.

5. **Стандартные Go runtime метрики** — встроены в `prometheus/client_golang`:
   - `go_goroutines`, `go_memstats_*`, `go_gc_duration_seconds`

6. **БД-метрики через pgx** — pgx имеет hook для query duration. Wrap'нуть в кастомный middleware чтобы было `db_query_duration_seconds{service, query_kind}`.

### B. Per-service business metrics

Каждый сервис добавляет свои **бизнес**-метрики (помимо стандартных RED).

#### `payment-service`
```
payments_invoice_created_total{provider, plan_id, currency}
payments_status_total{provider, status: pending|paid_db_only|paid_subscription_done|paid|failed|refunded|cancelled}
payment_webhook_duration_seconds{provider, result: ok|error}
payment_webhook_received_total{provider, signature_valid: true|false}
payment_state_transition_duration_seconds{from_status, to_status}
payment_pending_oldest_age_seconds{provider}  ← gauge, max age среди pending. Алерт если > 30 мин.
payment_sentinel_resumed_total{from_status, result}
provider_api_call_duration_seconds{provider, op: create|status|refund, success}
provider_api_errors_total{provider, op, error_kind: timeout|http_5xx|signature_invalid|...}
```

**Зачем именно эти:**
- `payment_pending_oldest_age_seconds` — главная метрика «чё-то стрёмное с платежами». Если алгоритм работает — pending не должны висеть > 30 мин.
- `payment_state_transition_duration_seconds` — видно где в state machine «застряло» (pending → paid_db_only обычно < 1с; если 30с — провайдер тормозит или у нас баг).
- `provider_api_errors_total{error_kind}` — раскладка по причинам помогает понять «всё плохо у Wata» vs «у Platega timeout'ы».

#### `vpn-service`
```
vpn_users_total                                        ← gauge всего юзеров
vpn_users_active                                       ← gauge активных (active_connections.last_seen > now - 5m)
xray_connection_state{server_id, server_name, state}   ← gauge (1 если в этом state, 0 иначе)
xray_request_duration_seconds{server_id, op}           ← histogram (AddUser, RemoveUser, AlterInbound, QueryStats)
xray_request_errors_total{server_id, op, error_kind}
vpn_load_percent{server_id, server_name}              ← gauge (0–100)
vpn_active_connections                                 ← gauge (count active_connections с last_seen > now-5m)
vpn_heartbeat_lag_seconds{server_id}                  ← gauge (now - last successful heartbeat по серверу)
vpn_resync_duration_seconds{server_id}                ← histogram
vpn_resync_users_total{server_id, result}             ← counter
vpn_create_user_duration_seconds{outcome: ok|partial|fail}  ← histogram (multi-server)
vpn_servers_active                                     ← gauge (count is_active=true)
```

**Зачем:**
- `xray_connection_state` — критическая. Алерт «один сервер не READY > 5 мин» — пользователь теряет географию.
- `vpn_heartbeat_lag_seconds` — если лаг > 3 минут, значит heartbeat не работает, и `active_connections` устаревает → device-limit может ломаться.
- `vpn_load_percent` — для бизнес-дашборда (где капасити кончается) и для алерта.

#### `subscription-service`
```
subscriptions_active                                   ← gauge (status='active' AND expires_at > now)
subscriptions_expiring_soon{horizon: 1h|24h|72h}      ← gauge (бизнес — кому скоро напомнить)
subscriptions_created_total{plan_id}                  ← counter
expire_cron_runs_total{result: ok|error}              ← counter
expire_cron_duration_seconds                          ← histogram
expire_cron_disabled_total{result: ok|error}          ← counter (сколько DisableVPNUser сделал)
expire_cron_last_success_timestamp                    ← gauge (для алерта «не запускался > 30 мин»)
channel_bonus_claims_total{result}                    ← counter
trial_used_total                                       ← counter
```

#### `auth-service`
```
auth_telegram_validations_total{result: ok|invalid_signature|expired|banned}
auth_jwt_issued_total
auth_users_total                                       ← gauge всего юзеров
auth_users_banned_total                                ← gauge ban'нутых (если фича используется)
```

#### `referral-service`
```
referral_links_created_total
referral_bonuses_total{type: subscription|withdrawal|...}
referral_relationships_total                           ← gauge (partners → referrals counts)
referral_balance_total_rubles                          ← gauge (сумма баланс по всем рефералам)
withdrawal_requests_total{status: pending|approved|rejected}
withdrawal_pending                                     ← gauge для алерта «висит выплата > 24ч»
```

#### `gateway`
```
http_requests_total{path, method, status, jwt_valid}
http_request_duration_seconds{path, method}
http_rate_limit_rejected_total{path, ip_or_user}
telegram_webhook_total{kind: bot|payment_provider, result}
subscription_token_requests_total                      ← публичный endpoint, отдельно мониторим
```

### C. Стандартные метрики из коробки

После подключения `prometheus/client_golang` бесплатно появляются:
- `go_goroutines` — goroutine leak detection
- `go_memstats_alloc_bytes` — heap usage
- `go_gc_duration_seconds` — GC pauses (если > 100ms — проблема)
- `process_cpu_seconds_total`, `process_resident_memory_bytes`

### D. Структура /metrics endpoint'ов

Каждый сервис открывает `:9090` (или `:9091`+) на отдельном HTTP-сервере. **Не на основном HTTP/gRPC порту**, чтобы:
- /metrics не светились наружу через Caddy (только Prometheus из внутренней сети дотянется)
- Падение основного HTTP не убивало /metrics (метрики до момента краха = критичны для разбора)

```
gateway          :8081 (HTTP)              :9081 (/metrics)
auth-service     :50060 (gRPC)             :9060 (/metrics)
subscription     :50061 (gRPC)             :9061 (/metrics)
vpn-service      :50062 (gRPC)             :9062 (/metrics)
payment-service  :50063 (gRPC)             :9063 (/metrics)
referral-service :50064 (gRPC)             :9064 (/metrics)
```

---

## 🚨 Алерты

### Принципы

1. **Алерт = действие.** Если на алерт нет инструкции что делать — это шум, выкинуть. У каждого алерта должен быть **runbook** (хотя бы 1 строка: «делать X»).
2. **Не алертить на симптомы, алертить на проблемы юзера.** «CPU 90%» — может быть штатно (cron). «p99 latency > 5s» — точно проблема юзера.
3. **Не больше 3-5 critical алертов в день в зрелой системе.** Если их больше — пороги некалиброваны, люди начинают игнорировать.
4. **Бизнес-метрики важнее технических.** «Регистраций за час 0 при норме 5» — важнее чем «CPU 60%».
5. **Severity levels:**
   - 🚨 **Critical** — будит ночью (Telegram звонок если возможно). Сервис недоступен.
   - ⚠️ **Warning** — Telegram канал. Деградация, может развиться.
   - ℹ️ **Info** — только в дашборде, не в Telegram.

### Конкретные алерты (минимальный набор первых 2 недель)

#### 🚨 Critical
1. **Backend down** — probe `https://api.osmonai.com/live` fails > 2 min. *Runbook:* проверить VPS, рестартнуть, начать failover.
2. **/ready 503** > 2 min. *Runbook:* открыть `/ready`, посмотреть какой апстрим down, проверить контейнер/БД.
3. **Postgres down** — `pg_up == 0` > 1 min. *Runbook:* `docker logs vpn-postgres`, рестарт, проверить диск.
4. **Disk usage > 90%** на любом VPS. *Runbook:* `du -sh /var/lib/docker/* /opt/backups/*`, чистка.
5. **Bot webhook не принимал update'ы > 5 min** (in_business_hours). *Runbook:* проверить TG webhook (`getWebhookInfo`), переустановить.
6. **Все Xray серверы down** — `sum(xray_servers_active) == 0`. *Runbook:* проверить exit-ноды.
7. **Backup Postgres старше 3 часов.** *Runbook:* проверить cron `pg-backup.sh`, прогнать руками, проверить SSH к бэкап-хосту (nl01).

#### ⚠️ Warning
8. **Один Xray-сервер не READY > 5 min** — `xray_connection_state{state="READY"} == 0` для конкретного `server_id`. *Runbook:* SSH к серверу, `docker logs xray`, рестарт.
9. **Pending платежи старше 30 мин > 0** — `payment_pending_oldest_age_seconds > 1800`. *Runbook:* посмотреть `SELECT * FROM payments WHERE status='pending' ORDER BY created_at`, разобрать.
10. **CPU > 80% > 5 min** на backend VPS. *Runbook:* `docker stats`, найти контейнер.
11. **RAM > 80% > 5 min**. *Runbook:* проверить swap (если будет настроен), найти течь.
12. **Postgres connections > 80% от max** (default 100). *Runbook:* `SELECT * FROM pg_stat_activity`, искать утечку. Признак — пора PgBouncer.
13. **ExpireCron не запускался > 30 мин** — `expire_cron_last_success_timestamp` старее 30 мин. *Runbook:* `docker logs vpn-subscription`, рестарт.
14. **Sentinel часто резолвит** — `rate(payment_sentinel_resumed_total[5m]) > 1/min`. *Runbook:* state machine зависает, искать в логах payment-service.
15. **HTTP 5xx rate > 1%** — `rate(http_requests_total{status=~"5.."}[5m]) / rate(http_requests_total[5m]) > 0.01`. *Runbook:* Loki по level=ERROR.
16. **Latency p99 > 2s** для `/api/v1/auth/validate` — критичная ручка для логина в Mini App.
17. **Xray load_percent > 90%** > 10 min на сервере. *Runbook:* добавить exit-ноду или поднять capacity.
18. **TLS-сертификат истекает < 7 дней** — `probe_ssl_earliest_cert_expiry - time() < 7 * 86400`.
19. **Регистраций за последний час 0** в business hours (08:00–23:00 по UTC+3). Норма — хотя бы 1/час сейчас. Если 0 — что-то сломалось но не алертит. *Только когда нагрузка стабильна, иначе будет шум — настроим после первой недели данных.*

#### ℹ️ Info (только дашборд)
- Новый Xray-сервер прошёл resync
- Большой всплеск регистраций (>3x за час против baseline)
- Sentinel впервые после долгого простоя что-то нашёл

### Доставка в Telegram

**Реализация:** Alertmanager → Telegram bot.

Варианты:
1. **alertmanager-bot** ([metalmatze/alertmanager-bot](https://github.com/metalmatze/alertmanager-bot)) — Telegram-бот, в чате `/start`, потом получает алерты. Простой setup.
2. **Прямой webhook через Telegram Bot API** — без отдельного сервиса, alertmanager шлёт сразу в `https://api.telegram.org/bot<TOKEN>/sendMessage`. Можно сделать через `httpconfig` или маленький релей-сервис.
3. **Grafana Alerting → Telegram channel** — встроено в Grafana, проще, не нужен Alertmanager. На MVP норм; для зрелого setup'а Alertmanager даёт больше (silences, inhibition rules).

**Рекомендация:** на MVP — **Grafana Alerting → Telegram channel** (1 час работы). Когда вырастем (>20 алертов одновременно) — переезд на Alertmanager.

**Telegram setup:**
- Создать **отдельного бота** `@maydavpn_alerts_bot` (НЕ основной `@maydavpnbot`) — чтобы не пересекалось с production webhook'ами.
- Создать **приватный канал** `MaydaVPN Alerts` куда добавить только Aziz'а (и Devin'а если нужно).
- Бот как админ канала.
- Алерты двух уровней:
  - **Critical** → один канал `MaydaVPN Alerts` + опционально дублировать в личку Aziz'у.
  - **Warning** → в тот же канал, но с другим тегом и тише (без `disable_notification: false`).

---

## 📈 Дашборды Grafana

Минимальный набор на старте — **6 дашбордов**:

### 1. **Overview** (главный, открывается с телефона)
Самый важный — Aziz открыл, за 30 секунд понял «зелёное / жёлтое / красное».
- Зелёный/красный индикатор: backend up, БД up, бэкап свежий
- Регистраций за последний час vs за вчерашний этот же час
- Активных подписок (gauge с trend'ом)
- Топ 3 ошибки за час из логов
- Текущий load_percent самого нагруженного сервера

### 2. **Service Health** (RED dashboard)
Для технического дебага.
- Per service: RPS, error rate, p50/p95/p99 latency
- gRPC client latency Gateway → каждый upstream
- Postgres queries: top by duration

### 3. **VPN Servers** (multi-server статус)
- xray_connection_state по каждому серверу (тепловая карта)
- load_percent timeline по серверам
- bandwidth IN/OUT per server
- active_connections / vpn_users_active
- heartbeat_lag по серверам

### 4. **Payments** (бизнес-критично)
- Invoice/час по провайдерам (stacked area)
- Конверсия pending → paid (по провайдерам)
- Pending старее 30 минут (table)
- Sentinel activity timeline
- Provider API errors heatmap

### 5. **Subscriptions** (бизнес)
- Активных подписок timeline
- Истекающих в 24/72 часа (numbers)
- ExpireCron last run + duration
- Channel bonus claim'ы

### 6. **Infrastructure** (host + DB)
- CPU/RAM/disk timeline по VPS
- Docker контейнеры: status, restart count, OOM events
- Postgres: connections, slow queries, размер БД
- Network IN/OUT

### Для будущего
- **Referrals** (когда юзеров > 1000)
- **Business KPI** (MRR, ARPU, churn, LTV — когда платящих > 100)
- **SLO Burn Rate** (когда введём SLO)

---

## 📝 Логи (Loki)

### Что собираем
- Stdout/stderr всех Docker-контейнеров (Promtail сам подцепится через docker-сокет)
- Уже структурированный JSON от zap (`{"level":"info","ts":...,"caller":"...","msg":"..."}`)
- Метки автоматом: `container`, `service`, `level`, `host`

### Что добавить в код
- **request_id** в zap-контексте (chi middleware уже даёт `X-Request-Id`, надо прокинуть в zap.With)
- **user_id** где доступен
- **provider** для платежей

### Retention
- 14 дней на диске. Старее — выгружать в S3-compatible (можно прямо в Selectel где будут бэкапы).
- На 10к юзеров логи будут ~5–10 GB / неделю — сжатый Loki поместится.

### Алерты на logs (через Grafana Alerting на Loki query)

```
# Резкий рост ERROR'ов
sum(rate({level="ERROR"}[5m])) > 0.5

# Появление panic'и где-либо
sum(count_over_time({}|~ "panic" [5m])) > 0

# Specific business: Wata signature mismatch — это попытка взлома или провайдер сломал ключи
sum(count_over_time({service="payment-service"}|~ "signature.*invalid" [5m])) > 3
```

---

## 🚀 Phased rollout (по неделям)

### Неделя 1 — Infrastructure baseline
**Цель:** видеть, что VPS жив. Это можно сделать быстро, без правки кода.

1. **Подготовить `fi02`:** добавить 1-2 GB swap, убрать/починить watchtower (см. раздел «Вариант B»).
2. **Настроить WireGuard mesh** между backend ↔ fi02 (приватная сеть для scrape'а `/metrics`).
3. Поднять `Prometheus + Grafana` в docker-compose на `fi02`.
4. Подключить `node_exporter` на backend, nl01 и fi02 — host-метрики.
5. Подключить `cadvisor` на backend — Docker-метрики.
6. Подключить `postgres_exporter` к `vpn-postgres` (scrape через WG).
7. Подключить `blackbox_exporter` на fi02 для probe `/live`, `/ready`, TLS, DNS (наружу, не через WG).
8. **Дашборд 6 (Infrastructure)** — host + DB. Импортируем community (1860 для node_exporter, 9628 для Postgres).
9. **Алерты Critical 1–7** (backend down, /ready 503, Postgres down, disk, bot webhook, Xray down, backup stale).
10. Telegram-канал `MaydaVPN Alerts` + бот `@maydavpn_alerts_bot`, проверить тестовый алерт.
11. Настроить публичный HTTPS для Grafana (`grafana.osmonai.com` → fi02 через Caddy + Let's Encrypt). Auth: сильный admin пароль + OAuth (опционально).

**Outcome:** при любой инфра-проблеме приходит Telegram-алерт. Время: 1–1.5 дня.

### Неделя 2 — Service RED metrics
**Цель:** видеть нагрузку и ошибки в коде.

1. Создать `platform/pkg/metrics` — HTTP middleware, gRPC interceptors, /metrics handler.
2. Подключить во всех 6 сервисах. Открыть :9080+ для каждого.
3. Добавить Prometheus targets для всех /metrics.
4. **Дашборд 2 (Service Health)** — RED по каждому сервису.
5. **Warning-алерты 8–11, 15–16** (5xx rate, latency p99, CPU/RAM).

**Outcome:** видно RPS/errors/latency по каждому endpoint'у. Время: 2 дня.

### Неделя 3 — Business metrics
**Цель:** видеть здоровье бизнеса, не только техники.

1. Кастомные business metrics в коде каждого сервиса (см. секцию «B. Per-service business metrics»).
2. **Дашборды 1 (Overview), 3 (VPN), 4 (Payments), 5 (Subscriptions)**.
3. **Warning-алерты 12–14, 17–19** (Postgres connections, ExpireCron, Sentinel, load_percent, регистрации).
4. Калибровка порогов на реальных данных за неделю.

**Outcome:** Aziz открывает Overview-дашборд и за 30 сек понимает состояние бизнеса. Время: 3 дня.

### Неделя 4 — Logs + tuning
**Цель:** агрегация логов + корректировка после первых пары инцидентов.

1. Поднять `Loki + Promtail`.
2. Добавить `request_id` / `user_id` в zap-контекст.
3. Алерты на лог-патерны.
4. Калибровка алертов: убрать шумные, добавить пропущенные.
5. Финальный аудит: каждый алерт имеет runbook.

**Outcome:** полноценный observability stack. Время: 2 дня.

### Позже (по триггерам, не «на всякий случай»)
- **OpenTelemetry traces** — когда появятся «чё-то висит, не понимаю где» проблемы.
- **VictoriaMetrics** — если retention > 30 дней или метрики > 100 GB.
- **SLO + error budgets** — когда платящих > 1000 и нужны бизнес-обязательства.
- **PagerDuty / Opsgenie** — когда команда > 1 человека и нужна on-call rotation.

---

## 💰 Стоимость

| Компонент | Стоимость |
|---|---|
| Prometheus, Grafana, Alertmanager, Loki | $0 (open source) |
| node_exporter, postgres_exporter, cadvisor, blackbox_exporter | $0 |
| Хостинг (на `fi02`, Hetzner 4GB Helsinki) | уже оплачен под Xray |
| Telegram bot | $0 |
| S3 для архива логов (опц.) | $1–2/мес |
| **Итого** | **$0–2/мес** |

Сравнение с SaaS:
- Datadog: ~$75/мес минимум
- New Relic: ~$25/мес
- Better Stack: ~$25/мес
- **Self-hosted экономит ~7000₽/мес.**

Минусы self-hosted: нужно поддерживать обновления (раз в полгода). Зато никаких vendor lock-in.

---

## ⚖️ Что важно понимать про мониторинг

### Не пытаться мониторить всё сразу
Каждая метрика — это:
- Disk space в Prometheus (cardinality bomb)
- RAM в Grafana при отрисовке
- Время на калибровку алертов
- Шум который мешает увидеть важное

**Правило:** добавлять метрику только если:
1. Знаешь как использовать (какой алерт, какой график)
2. Готов её поддерживать (имена, типы, метки)

### Cardinality bomb
Худшее что можно сделать в Prometheus — добавить метрику с лейблом `user_id`. Это = N новых time series, которые останутся навсегда. Прометей может встать на коленях при 10к юзеров.

**Правила меток:**
- ✅ `provider` (~5 значений), `status` (~10), `path` (~50), `server_id` (~20)
- ❌ `user_id`, `payment_id`, `request_id` — high cardinality, в логи (Loki) можно

### Алерты ≠ дашборды
- Дашборд — ретроспективный анализ, для разбора.
- Алерт — здесь и сейчас, требует действия.
- Делать их раздельно. Не каждая метрика заслуживает алерта.

### Качество > количество
Если из 50 алертов в день 49 ложные — никто не реагирует на 50-й, который настоящий. Лучше 5 надёжных алертов чем 50 сомнительных.

---

## 🗂 Связь с остальным roadmap'ом

| Задача в `00-ha-scaling-roadmap.md` | Что даст этот план |
|---|---|
| **0.2** Prometheus + Grafana + Telegram alerts | ← этот план реализует |
| **0.3** Loki + Promtail | ← этот план реализует |
| **1.6** Failover-скрипт | Нужны метрики и алерты для триггера |
| **1.7** Health-check daemon | UptimeRobot/blackbox-exporter — наш choice |
| **2.6** Cloudflare Load Balancer | health-check'и от CF — мониторим из этого плана |

---

## 📋 Чек-лист перед стартом реализации

Прежде чем начать неделю 1, проверить/сделать:

### Хост `fi02`
- [x] **Ресурсы** (2026-05-03): 3.7 GB RAM (2.4 GB free) / 38 GB disk (20 GB free) / 2 vCPU — ✅ хватит
- [ ] **Swap:** добавить 1-2 GB (сейчас 0 B)
  ```bash
  ssh fi02 'fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile && echo "/swapfile none swap sw 0 0" >> /etc/fstab'
  ```
- [ ] **Watchtower:** разобраться с crash-loop и либо починить (с окном обновлений), либо удалить (`docker rm -f watchtower`)
- [ ] **Firewall:** на fi02 проверить что 9090 (Prometheus), 3000 (Grafana), 9100 (node_exporter) закрыты снаружи (только через WG или localhost)

### Сеть
- [ ] **WireGuard mesh** backend ↔ fi02:
  - Сгенерить ключи на обоих хостах
  - Приватная сеть `10.10.0.0/24` (backend = `.1`, fi02 = `.2`, nl01 = `.3` для будущего)
  - Prometheus scrape-targets: `http://10.10.0.1:9100/metrics` (и т.д.)
- [ ] **DNS:** добавить `grafana.osmonai.com → fi02 IP` (A-запись)

### Telegram
- [ ] Создан ли отдельный бот для алертов (`@maydavpn_alerts_bot`) через `@BotFather`
- [ ] Создан ли приватный канал `MaydaVPN Alerts`, бот добавлен админом
- [ ] Известен ли `chat_id` канала (получить через `curl https://api.telegram.org/bot<TOKEN>/getUpdates`)

### Grafana
- [ ] Сильный admin-пароль сгенерирован и сохранён в 1Password/bitwarden
- [ ] (Опционально, позже) OAuth через GitHub/Google для 2FA

### Loki (неделя 4, не сейчас)
- [ ] Решено где хостить Loki — **на fi02** (там же где остальной стек, свободных ресурсов ещё ~1 GB)

---

## 🔗 Полезные ссылки

- [Prometheus best practices: Naming](https://prometheus.io/docs/practices/naming/)
- [USE method](http://www.brendangregg.com/usemethod.html) — Brendan Gregg
- [The RED method](https://thenewstack.io/monitoring-microservices-red-method/) — Tom Wilkie
- [Google SRE book — Monitoring](https://sre.google/sre-book/monitoring-distributed-systems/)
- [Awesome Prometheus alerts](https://samber.github.io/awesome-prometheus-alerts/) — готовые алерты для всего
- [pgwatch2](https://pgwatch.com/) — альтернатива postgres_exporter, побогаче
- [Grafana Loki](https://grafana.com/oss/loki/) — документация
- [Promtail Docker config](https://grafana.com/docs/loki/latest/clients/promtail/configuration/)
- [alertmanager-bot](https://github.com/metalmatze/alertmanager-bot) — Telegram-бот для AM

---

## 📝 Принципы (зачем именно так)

1. **Sequential, не all-at-once.** Неделя 1 → 2 → 3 → 4. Каждая фаза самодостаточна и приносит пользу.
2. **Self-hosted, не SaaS.** Контроль над данными, нет vendor lock-in, экономия 7к₽/мес.
3. **Бизнес > техники.** В алертах в первую очередь — «работает ли flow юзера», во вторую — «здорова ли железка».
4. **Каждый алерт имеет runbook.** Без этого алерт = шум.
5. **Не более 5 critical алертов в день.** Калибруем пороги, отключаем шумные.
6. **Метки разумной cardinality.** Никаких user_id в метриках, только в логах.
7. **Параллельный мониторинговый VPS.** Не на том же что мониторим.
