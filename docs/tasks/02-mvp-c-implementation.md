# 02. MVP Вариант C — План реализации

**Дата:** 2026-04-22  
**Статус:** 🔵 В работе  
**Автор:** Devin + aziz  
**Родительский документ:** [01-mvp-plan.md](./01-mvp-plan.md)

---

## 🎯 Цель

Довести `vpn_go` до состояния "готов к приёму реальных денег от публичных пользователей": автоматическая оплата, реферальная программа, multi-server, админка.

**Оценка:** 10-14 дней работы.

---

## 📦 Результат (Definition of Done)

- [ ] Публичный пользователь открывает Telegram Mini App → логинится через Telegram
- [ ] Выбирает тариф (1/3/6/12 мес) и число устройств
- [ ] Оплачивает **автоматически** (Telegram Stars или ЮKassa)
- [ ] После оплаты получает **рабочую VLESS-ссылку** (реальные Reality-ключи, реальный сервер)
- [ ] Подключается с iPhone/Android/PC — интернет работает через VPN
- [ ] Может выбрать локацию из **2-3 серверов** (разные страны)
- [ ] **Лимит устройств** реально работает (4-е подключение отбивается)
- [ ] Пригласил друга по реферальной ссылке → оба получили бонус +3 дня
- [ ] Админ видит в админке: юзеров, подписки, платежи. Может банить.
- [ ] Всё задеплоено, SSL, домен, мониторинг

---

## 🗺️ Дорожная карта (11 этапов)

### Этап 0 — Инфра и решения *(до старта кодинга)*

**Решения приняты (2026-04-22):**
- ✅ **Оплата:** Telegram Stars (без ЮKassa — нет ИП, Stars проще)
- ✅ **Xray серверы:** Начинаем с **1 VPS** (масштабируемся позже после первых продаж)
- ✅ **Backend хостинг:** **Отдельный VPS** (backend отдельно от Xray — безопаснее)
- ✅ **Домен:** пока **Cloudflare Tunnel** (дешевле для разработки, купим настоящий ближе к релизу)

**Доп. решение (2026-04-22):** Начинаем разработку **без VPS** — всё локально/в Docker на текущей машине. VPS покупаем на **Этапе 9 (Deploy)**.

**Что осталось сделать:**
- [ ] Запустить **локально Xray + Reality** в Docker для разработки/тестов (имитация будущего VPS)
- [ ] Создать GitHub репозиторий (если ещё нет) + секреты для CI
- [ ] У `@maydavpnbot` проверить: включены ли платежи? (через @BotFather → Payments → Telegram Stars)
- [ ] **К Этапу 9:** купить 2 VPS (backend + xray)

**Следствие для плана:**
- Этап 2 (Xray) — интеграция пишется против **локального Xray в Docker**. Когда появится VPS — меняем только host/keys в БД.
- Этап 6 (Multi-server) — только подготовка архитектуры. Второй сервер — после первых продаж.
- Этап 5 (Payment) — упростился: только Telegram Stars, без ЮKassa.
- Этап 9 (Deploy) — станет длиннее (+1 день на покупку/настройку VPS).

---

### Этап 1 — Фундамент (1-2 дня) ✅ ЗАВЕРШЁН
- [x] ✅ Dockerfile для каждого сервиса (multi-stage, alpine base) — **auth, subscription, vpn, gateway**
- [x] ✅ `.dockerignore` в корне
- [x] ✅ `.env.example` с полным набором переменных
- [x] ✅ Единый `deploy/compose/docker-compose.yml` (postgres + migrate + 4 сервиса)
- [x] ✅ Дописаны `.down.sql` миграции для `subscription-service` и `vpn-service`
- [x] ✅ `Taskfile.yaml` расширен: `build:{auth,sub,vpn,gateway}`, `build-all`, `dev:*`, `run-bin:*`, `compose:{up,down,logs,ps,rebuild}`, `db:{up,down,psql}`, `migrate:{up,down}`, `stop-all`, `clean`
- [x] ✅ Схема портов синхронизирована: VPN Service = **50057** в коде, compose, `.env.example` и `ARCHITECTURE.md`
- [x] ✅ `cp .env.example .env` → `docker compose up` — весь стек поднимается (postgres healthy, migrate exit 0, 4 сервиса Up, `GET /api/v1/subscriptions/plans` возвращает JSON)
- [x] ✅ `task build-all` собирает все 4 сервиса на хосте
- [x] ✅ **Багфиксы по ходу:**
  - Убран `COPY go.work go.work.sum` из всех Dockerfile, добавлено `ENV GOWORK=off` — сервисы теперь собираются по своим go.mod с `replace` на `../../platform` и `../../shared`
  - Порт postgres параметризован `POSTGRES_HOST_PORT` (дефолт 5432, в примере — 5433, т.к. у dev-хоста 5432 занят)
  - Per-service `schema_migrations` (`auth_schema_migrations`, `subscription_schema_migrations`, `vpn_schema_migrations`) — иначе golang-migrate считал одну версию "1" общей и пропускал миграции subscription/vpn

- [x] ✅ **Рефакторинг env-структуры по образцу `eng_go`:**
  - Мастер `deploy/env/.env.template` со ВСЕМИ переменными с префиксами `AUTH_*`, `SUB_*`, `VPN_*`, `GATEWAY_*`, `PAYMENT_*`, `REFERRAL_*`, `ADMIN_*` (+ резерв портов)
  - Per-service шаблоны `deploy/env/{auth,sub,vpn,gateway}.env.template` с `${AUTH_GRPC_PORT}` плейсхолдерами
  - `deploy/env/generate-env.sh` + `envsubst` (a8m/envsubst, ставится task'ом в `./bin/envsubst`) — раскрывает плейсхолдеры в чистые `GRPC_PORT/DB_HOST/...` per-service env-файлы
  - `docker-compose.yml` читает мастер и мапит префиксы → чистые переменные в `environment:` секциях контейнеров
  - `Taskfile`: `env:install-envsubst`, `env:generate`, `dev:*` с `dotenv: [../../deploy/env/<svc>.env]`, `compose:up` автоматически зависит от `env:generate`
  - **gRPC порты перенумерованы с 50060:** auth=50060, sub=50061, vpn=50062, payment=50063, referral=50064, admin=50065 (gateway HTTP=8081). Дефолты в `config.go`, `Dockerfile`, `docker-compose.yml`, `ARCHITECTURE.md` синхронизированы.
  - Старые `.env` и `.env.example` в корне удалены; `.gitignore` уже корректно игнорирует `*.env` с исключением `!*.env.template`

**Что создано и исправлено в этой сессии (2026-04-22):**
```
vpn_go/
├── .dockerignore
├── .gitignore                                              ← *.env, !*.env.template
├── Taskfile.yaml                                           ← env:*, dev:*, compose:*, migrate:*
├── deploy/
│   ├── env/                                                ← NEW (вся env-структура)
│   │   ├── .env.template                                   ← мастер (AUTH_/SUB_/VPN_/GATEWAY_/...)
│   │   ├── .env                                            ← gitignored, создаётся env:generate
│   │   ├── auth.env.template, sub.env.template,            ← плейсхолдеры ${AUTH_…}
│   │   │   vpn.env.template, gateway.env.template
│   │   ├── auth.env, sub.env, vpn.env, gateway.env         ← gitignored, генерируются
│   │   └── generate-env.sh                                 ← envsubst-обёртка
│   └── compose/docker-compose.yml                          ← читает deploy/env/.env
├── bin/envsubst                                            ← gitignored, ставится env:install-envsubst
├── services/auth-service/
│   ├── Dockerfile                                          ← EXPOSE 50060, GOWORK=off
│   └── internal/config/config.go                           ← default GRPC_PORT=50060
├── services/subscription-service/
│   ├── Dockerfile                                          ← EXPOSE 50061, GOWORK=off
│   ├── internal/config/config.go                           ← default GRPC_PORT=50061
│   └── migrations/001_create_subscriptions.down.sql        ← NEW
├── services/vpn-service/
│   ├── Dockerfile                                          ← EXPOSE 50062, GOWORK=off
│   ├── internal/config/config.go                           ← default GRPC_PORT=50062
│   └── migrations/001_create_vpn_tables.down.sql           ← NEW
├── services/gateway/
│   ├── Dockerfile                                          ← GOWORK=off
│   └── internal/config/config.go                           ← default addrs :50060/:50061/:50062
└── docs/
    ├── ARCHITECTURE.md                                     ← порты 50060-50065, Subscription вместо Traffic
    └── tasks/02-mvp-c-implementation.md                    ← этот файл
```

**Шпаргалка команд:**
- `task env:generate` — создать мастер `.env` из шаблона + per-service env-файлы
- `task compose:up` — поднять весь стек (env:generate + build + up)
- `task compose:down` / `task compose:down-v` — остановить (второй удаляет том postgres)
- `task dev:auth` / `dev:sub` / `dev:vpn` / `dev:gateway` — `go run` локально с per-service .env
- `task build-all` — собрать бинарники в `./bin/`
- `task migrate:up` / `migrate:down` — накатить/откатить миграции (требует запущенного стека)
- `task db:psql` — psql в контейнере postgres

**Проверки:**
- `task build-all` → 4 бинаря в `./bin/`
- `task compose:up` → 5 контейнеров Up, postgres healthy
- `curl http://localhost:8081/health` → `{"status":"ok","service":"vpn-gateway"}`
- `curl http://localhost:8081/api/v1/subscriptions/plans` → JSON с 4 тарифами
- `\dt` в БД → `users`, `subscription_plans`, `device_addon_pricing`, `subscriptions`, `vpn_servers`, `vpn_users`, `active_connections` + 3 migration-таблицы

---

### Этап 2 — Xray интеграция (2-3 дня) ✅ ЗАВЕРШЁН (локально)
- [x] ✅ Xray + Reality развёрнут **локально в Docker** (`ghcr.io/xtls/xray-core:latest`, порт 10085 для gRPC API, 8443 для VLESS), не на VPS (по решению Этапа 0)
- [x] ✅ Сгенерированы Reality x25519 keys (`xray x25519`) + random 8-byte `short_id` (`openssl rand -hex 8`), положены в `deploy/env/.env.template` как dev-значения
- [x] ✅ Xray config-шаблон `deploy/compose/xray/config.json.template` с `inbound[api]` + `inbound[vless-reality-in]` (VLESS + Reality, dest=github.com:443, SNI=github.com). `env:generate` подставляет ключи через envsubst → `config.json` (gitignored).
- [x] ✅ `platform/pkg/xray/client.go` — Go-клиент Xray API:
  - `New(ctx, addr)` — gRPC dial через insecure (внутренний интерфейс)
  - `AddUser(ctx, inboundTag, uuid, email, flow)` — `HandlerService.AlterInbound` + `AddUserOperation` с VLESS `Account`
  - `RemoveUser(ctx, inboundTag, email)` — `RemoveUserOperation`
  - `GetUserStats(ctx, email, reset)` — `StatsService.QueryStats` с pattern `user>>>{email}>>>traffic`
  - Использует официальные proto из `github.com/xtls/xray-core v1.260327.0`
- [x] ✅ VPN Service подключается к Xray API при старте (`app.initXray`), в `CreateVPNUser` реально регистрирует юзера во всех активных серверах (идемпотентно — игнорирует "already exists")
- [x] ✅ `vpn_servers`: миграция 002 добавила колонку `inbound_tag` + UNIQUE(name). Mock-сервера из 001 удалены. Реальный "Local Xray (dev)" upsert-ится при старте VPN Service из env (`XRAY_PUBLIC_HOST`, `XRAY_REALITY_*`, `XRAY_INBOUND_TAG`).
- [x] ✅ **e2e подтверждён:**
  - `grpcurl CreateVPNUser {user_id:1, subscription_id:1}` → получили UUID
  - Логи VPN Service: `xray user added server_id=5 inbound_tag=vless-reality-in`
  - Логи Xray: входящий запрос на API inbound
  - `GetVLESSLink {user_id:1, server_id:5}` → валидная `vless://UUID@localhost:8443?...`
  - Отдельный `xray-client` контейнер с этой ссылкой (UUID + pbk + sid + sni) поднял SOCKS5 inbound на `:1080`
  - `curl --socks5 vpn-xray-client:1080 https://ipinfo.io` → настоящий ответ с IP хоста — **трафик реально идёт через Reality handshake**
- [x] ✅ **Весь стек переведён на Go 1.26** (`go.work` + все 6 `go.mod` с `go 1.26.0` + `toolchain go1.26.2`; Dockerfile'ы → `golang:1.26-alpine`) — это требование `xtls/xray-core@v1.260327.0`.

**Что создано/изменено:**
```
platform/
├── go.mod                                                   ← go 1.26.0 + toolchain + xtls/xray-core
├── go.sum                                                    ← ~60 транзитивов от xray-core (reality, quic-go, utls, sing, ...)
└── pkg/xray/client.go                                        ← NEW (Client + AddUser/RemoveUser/GetUserStats)

deploy/
├── compose/
│   ├── docker-compose.yml                                    ← + service "xray", depends_on: vpn-service → xray
│   └── xray/
│       ├── config.json.template                              ← NEW (Reality inbound + API inbound)
│       └── config.json                                       ← NEW, gitignored (envsubst из мастер-env)
└── env/
    ├── .env.template                                         ← + VPN_XRAY_REALITY_{PRIVATE,PUBLIC}_KEY, SHORT_ID, DEST, SNI, PUBLIC_HOST, VLESS_PORT, INBOUND_TAG
    ├── vpn.env.template                                      ← + XRAY_* per-service env
    └── generate-env.sh                                       ← + мёрдж новых ключей в существующий .env + xray config.json

services/vpn-service/
├── Dockerfile                                                ← golang:1.26-alpine, EXPOSE 50062
├── migrations/002_add_inbound_tag_and_clear_seed.{up,down}.sql  ← NEW
├── internal/config/config.go                                 ← + XrayConfig (APIHost/Port, InboundTag, PublicHost, VLESSPort, Reality*)
├── internal/model/vpn.go                                     ← + VPNServer.InboundTag
├── internal/repository/vpn.go                                ← + UpsertServerByName, inbound_tag в ListServers/GetServer
├── internal/service/vpn.go                                   ← + xray.Client в DI, CreateVPNUser вызывает AddUser по всем активным
└── internal/app/app.go                                       ← + initXray, seedLocalServer, reflection.Register для dev

Taskfile.yaml                                                 ← + xray:{up,restart,logs,genkeys}
```

**Шпаргалка — что появилось:**
- `task xray:genkeys` — сгенерить новые Reality keys + short_id (вывод в stdout)
- `task xray:restart` — применить изменённый config.json (после правки шаблона + `task env:generate`)
- `task xray:logs` — tail логов Xray
- `task compose:up` — поднимает и Xray в том числе (автоматически)

**Что осознанно НЕ сделано на этом этапе** (перенесено):
- Реальный iPhone-клиент (V2BoxLite) — невозможно без VPS с публичным IP (localhost недоступен с телефона). Проверено Linux-клиентом xray внутри docker-сети.
- `DeleteVPNUser` (физическое удаление юзера из Xray) — сейчас есть только `DisconnectDevice`, который чистит только запись active_connection. Физический `RemoveUser` по закрытию подписки сделаем на Этапе 3 / после.
- `GetUserStats` фоновая задача (heartbeat для `active_connections.last_seen`) — это целевой приз Этапа 3 (Device Limit).

---

### Этап 3 — Device Limit (0.5 дня) ✅ ЗАВЕРШЁН
- [x] ✅ `active_connections.last_seen` используется как heartbeat-время последней активности устройства
- [x] ✅ Фоновая горутина `service.Heartbeat` (в `services/vpn-service/internal/service/heartbeat.go`): каждые 60с опрашивает `xray.GetUserStats(email, reset=false)` для всех vpn_users, сравнивает с предыдущим значением суммы uplink+downlink, и если трафик вырос — `UPDATE active_connections SET last_seen=NOW() WHERE vpn_user_id=X`. Запускается в `app.Start()`, корректно останавливается при shutdown через `closer`.
- [x] ✅ При выдаче VLESS-ссылки (`GenerateVLESSLink(userID, serverID, deviceIdentifier)`):
  - JOIN c `subscriptions` → max_devices активной подписки
  - если устройство НОВОЕ (нет такого device_identifier в active_connections) → `COUNT(WHERE last_seen > NOW() - 5min)` должен быть < max_devices
  - если ИЗВЕСТНОЕ → slot не увеличивается, просто `UPSERT last_seen=NOW()`
  - окно `DeviceActivityWindow = 5 * time.Minute`
- [x] ✅ Если лимит превышен — `service.ErrDeviceLimitExceeded` → gRPC `codes.ResourceExhausted` → Gateway → **HTTP 429** `{"error":"device_limit_exceeded","message":"device limit exceeded: 2/2 devices active"}`
- [x] ✅ Ручное отключение устройства: `DELETE /api/v1/vpn/devices/:connectionId` → gRPC `DisconnectDevice` → `DELETE FROM active_connections WHERE id = ...` (slot освобождается мгновенно)
- [x] ✅ Proto обновлён: `GetVLESSLinkRequest.device_identifier`, `GetVLESSLinkResponse.current_devices/max_devices/connection_id`. Регенерирован через `task proto:gen` (плагины `protoc-gen-go` + `protoc-gen-go-grpc` ставятся в `./bin` автоматически).
- [x] ✅ Миграция `active_connections` не понадобилась — UNIQUE `(vpn_user_id, device_identifier)` уже был в миграции 001, `ON CONFLICT` в `UpsertActiveConnection` работает.

**e2e подтверждён (всё в одной сессии):**

```bash
# 1. seed: user_id=1, подписка с max_devices=2
INSERT users, subscriptions → id=1

# 2. CreateVPNUser → UUID=c8b191e9-…, добавлен в Xray

# 3. Сценарий лимита:
curl /api/v1/vpn/servers/5/link?user_id=1&device_id=iPhone   # 200, current=1/max=2
curl /api/v1/vpn/servers/5/link?user_id=1&device_id=PC       # 200, current=2/max=2
curl /api/v1/vpn/servers/5/link?user_id=1&device_id=Tablet   # 429 "device_limit_exceeded"
curl /api/v1/vpn/servers/5/link?user_id=1&device_id=iPhone   # 200 (повтор известного → не слот)
curl -X DELETE /api/v1/vpn/devices/2                         # удалить PC
curl /api/v1/vpn/servers/5/link?user_id=1&device_id=Tablet   # 200, current=2/max=2

# 4. Реальный Heartbeat:
# (a) поднять xray-client с UUID, прогнать curl --socks5 … ipinfo.io  → ответ пришёл
# (b) подождать 60с
# (c) логи vpn-core: {"msg":"heartbeat tick","users_checked":1,"refreshed":1}
# (d) active_connections: last_seen обновился с 09:14:03 → 09:17:07 ✓
```

**Что создано/изменено:**
```
shared/proto/vpn/v1/vpn.proto                                  ← +device_identifier, +current/max_devices
shared/pkg/proto/vpn/v1/vpn.pb.go                              ← регенерирован

services/vpn-service/
├── internal/repository/vpn.go                                 ← +CountActiveDevices, UpsertActiveConnection,
│                                                                 UpdateLastSeenByVPNUser, GetSubscriptionMaxDevices,
│                                                                 ListAllVPNUsers
├── internal/service/vpn.go                                    ← GenerateVLESSLink: +deviceIdentifier,
│                                                                 возвращает VLESSLinkResult{Link, Server,
│                                                                 ConnectionID, CurrentDevices, MaxDevices}
├── internal/service/heartbeat.go                              ← NEW (Heartbeat{prevSeen}, Run(ctx), tick)
├── internal/api/vpn.go                                        ← GetVLESSLink ловит ErrDeviceLimitExceeded
│                                                                 → codes.ResourceExhausted
└── internal/app/app.go                                        ← +service.Heartbeat, go a.heartbeat.Run(hbCtx)

services/gateway/
├── internal/client/vpn.go                                     ← GetVLESSLink(+deviceID), +DisconnectDevice
├── internal/handler/vpn.go                                    ← device_id/user_id query-params,
│                                                                 429 на ResourceExhausted,
│                                                                 +DisconnectDevice handler
└── internal/app/app.go                                        ← +DELETE /api/v1/vpn/devices/{connectionId}

Taskfile.yaml                                                  ← +proto:install-plugins, fix proto:gen (find -print0)
bin/protoc-gen-go, bin/protoc-gen-go-grpc                      ← gitignored, ставятся task'ом

docs/services/device-limit.md                                  ← NEW (диаграммы + сценарии + ограничения модели)
docs/services/README.md                                        ← + ссылка на device-limit.md
```

**Шпаргалка — новые API:**
- `GET /api/v1/vpn/servers/:serverId/link?device_id=iPhone&user_id=1`
  - 200 → `{"vless_link":"…","current_devices":1,"max_devices":2,"connection_id":1,"server":{…}}`
  - 429 → `{"error":"device_limit_exceeded","message":"device limit exceeded: 2/2 devices active"}`
- `DELETE /api/v1/vpn/devices/:connectionId`
  - 200 → `{"success":true,"connection_id":N}`

**Что осознанно НЕ сделано (оставлено на будущее):**
- UUID per device — пока один UUID на юзера, поэтому heartbeat обновляет last_seen для **всех** устройств юзера одновременно (ограничение модели, описано в `docs/services/device-limit.md`). Для hard-limit нужно переделать модель — не в MVP.
- Автоматическое удаление VLESS-юзера из Xray при окончании подписки (`xray.RemoveUser`) — закроется на Этапе 5 (Payment) вместе с cron-задачей проверки истёкших подписок.
- Re-seed юзеров в Xray после рестарта контейнера vpn-xray — TODO, пока не критично (рестартов нет).

---

### Этап 4 — Auth Middleware в Gateway (0.5 дня) ✅ ЗАВЕРШЁН
- [x] ✅ `platform/pkg/middleware/jwt.go` — `JWTMiddleware(secret)` (chi-совместимая): парсит `Authorization: Bearer <jwt>`, проверяет HS256-подпись + `exp`, кладёт `userID`/`role` в context. Ошибки различаются: `missing_token` / `invalid_scheme` / `invalid_signature` / `token_expired` → 401 JSON `{error, message}`. Хелперы `UserIDFromContext(ctx)`, `RoleFromContext(ctx)`.
- [x] ✅ В Gateway роуты разделены на два слоя: **публичные** (`/health`, `POST /auth/validate`, `GET /subscriptions/plans`, `GET /subscriptions/plans/:id/pricing`) и **защищённые** (всё остальное через `r.Group(func(r) { r.Use(jwtMw); ... })`).
- [x] ✅ JWT приходит в `Authorization: Bearer <token>`, парсится middleware'ом.
- [x] ✅ В `context.Context` кладутся `userID` + `role`. Все handler'ы теперь используют `userIDFromRequest(w, r)` (helper в `handler/context.go`). Убраны все `userID := int64(1)` и query-параметр `?user_id=` из защищённых ручек.
- [x] ✅ Auth Service уже умел `GenerateJWT(userID, role, TTL)` и возвращал `jwt_token` в `ValidateTelegramUser` — дополнительных изменений не потребовалось.
- [x] ✅ `JWT_SECRET` — общий между Auth Service и Gateway, в env мастер `AUTH_JWT_SECRET` → `gateway.env.template` мапит на `JWT_SECRET=${AUTH_JWT_SECRET}` → `docker-compose.yml` прокидывает в оба контейнера. Gateway конфиг валидирует наличие секрета при старте.

**e2e — 6 сценариев безопасности пройдено:**

```bash
# Публичные (без токена)
curl /health                              → 200
curl /api/v1/subscriptions/plans          → 200

# Защищённые без токена
curl /api/v1/vpn/servers/5/link?device_id=iPhone
    → 401 {"error":"missing_token","message":"Authorization header required"}

# Защищённые с ФЕЙКОВОЙ подписью
curl -H "Authorization: Bearer eyJ...FAKE..."  /api/v1/vpn/servers/5/link
    → 401 {"error":"invalid_signature","message":"invalid token: ..."}

# С ИСТЁКШИМ токеном (exp < now)
curl -H "Authorization: Bearer <expired>" /api/v1/subscriptions/active
    → 401 {"error":"token_expired","message":"JWT token has expired"}

# С валидным токеном
curl -H "Authorization: Bearer <valid>" /api/v1/vpn/servers/5/link?device_id=iPhone
    → 200, user_id взят из токена (1), connection_id=1

# ⚠️ Попытка подмены user_id через query
curl -H "Authorization: Bearer <user_id=1>" \
     /api/v1/vpn/servers/5/link?device_id=iPhone&user_id=99
    → 200, но в БД active_connections.vpn_user_id = 1 (НЕ 99)
    — query user_id ПРОИГНОРИРОВАН, источник userID только токен ✓
```

**Что создано/изменено:**
```
platform/
├── go.mod                                           ← +github.com/golang-jwt/jwt/v5 v5.2.1
└── pkg/middleware/jwt.go                            ← NEW (JWTMiddleware + UserIDFromContext + RoleFromContext)

services/gateway/
├── internal/config/config.go                        ← +JWTConfig{Secret}, +Validate()
├── internal/app/app.go                              ← +authmw импорт, +cfg.Validate() в New(),
│                                                       разделение роутов на публичные vs r.Group+jwtMw
├── internal/handler/context.go                      ← NEW (userIDFromRequest helper)
├── internal/handler/vpn.go                          ← убраны userID=int64(1), ?user_id=
└── internal/handler/subscription.go                 ← убраны userID=int64(1) (3 места)

deploy/env/gateway.env.template                      ← +JWT_SECRET=${AUTH_JWT_SECRET}
deploy/compose/docker-compose.yml                    ← gateway env: +JWT_SECRET: ${AUTH_JWT_SECRET}

docs/services/auth-middleware.md                     ← NEW → статус "реализовано"
docs/services/README.md                              ← + ссылка на auth-middleware.md
```

**Критический bug-fix:** до Этапа 4 любой мог послать `?user_id=5` и получить VPN-ссылку чужого юзера. Теперь `user_id` берётся **только** из подписанного токена, query-параметр игнорируется. Дыра закрыта.

**Что осознанно НЕ сделано:**
- Refresh-токены — пока один JWT с TTL=168h (7 дней). Когда истечёт — Mini App сам дёрнет `/auth/validate` с свежим initData и получит новый токен. Refresh-pattern добавим если начнут жаловаться, пока не критично.
- Role-check для админских ручек (`role == "admin"`) — будет на Этапе 8 (Admin Service).
- Revoke / blacklist токенов — пока нет. Бан юзера через `users.is_banned = true` + проверка в middleware (TODO на будущее).

---

### Этап 5 — Payment Service (2-3 дня) ✅ ЗАВЕРШЁН
Выбран: **Telegram Stars** (по решению Этапа 0). Payment Service — единственный сервис в MVP, который общается с внешним Telegram Bot API.

**Что реализовано (полный список):**

- [x] ✅ `services/payment-service/` — новый микросервис на порту **50063**, префикс `PAYMENT_*`
  - `cmd/main`, `internal/{app,api,service,repository,config,model}` — шаблон как у других сервисов
  - Dockerfile (`golang:1.26-alpine`, `ENV GOWORK=off`, EXPOSE 50063)
  - Добавлен в `go.work`
- [x] ✅ Proto `shared/proto/payment/v1/payment.proto`:
  - `CreateInvoice(user_id, plan_id, max_devices)` → `{payment_id, invoice_link, amount_stars}`
  - `GetPayment(payment_id)` / `ListUserPayments(user_id, limit, offset)`
  - `HandleTelegramUpdate(raw_json)` → `{handled, action}` (action: pre_checkout_ok / paid / paid_duplicate / refunded / ignored)
- [x] ✅ Миграция `services/payment-service/migrations/001_create_payments.{up,down}.sql`:
  таблица `payments` с `UNIQUE(external_id)` для идемпотентности + CHECK-constraint на `status IN (pending/paid/failed/refunded)` + индексы
- [x] ✅ Миграция `services/subscription-service/migrations/002_add_stars_price.up.sql`:
  +`price_stars INT` в `subscription_plans` и `device_addon_pricing` + seed 16 значений
  (1-12 мес × 2/5/10 устройств, от 100⭐ до 2800⭐)
- [x] ✅ Расширен `DevicePrice` в proto: +`price_stars`, +`plan_name` (для UI)
- [x] ✅ `platform/pkg/telegram/client.go` — тонкий HTTP-клиент над Bot API:
  `createInvoiceLink` / `answerPreCheckoutQuery` / `refundStarPayment` (последний на будущее)
- [x] ✅ **Создание инвойса** (`POST /api/v1/payments`, защищено JWT):
  1. `sub.GetDevicePricing(plan_id)` → находит `price_stars` для нужного `max_devices`
  2. `INSERT payments (status='pending')` → получает payment_id
  3. `tg.createInvoiceLink(payload=payment_id, currency="XTR", prices=[...])` → возвращает `t.me/$…` ссылку
  4. Mini App открывает через `Telegram.WebApp.openInvoice(link)`
- [x] ✅ **Webhook** (`POST /api/v1/telegram/webhook`, публичный + защищён shared-секретом):
  - Header `X-Telegram-Bot-Api-Secret-Token` сверяется с env `TELEGRAM_WEBHOOK_SECRET`; без совпадения → 403
  - **pre_checkout_query** → проверка что payment ещё `pending` → `tg.answerPreCheckoutQuery(ok=true)`
  - **successful_payment** → **идемпотентный** handler:
    1. Проверка дубликата через `GetByExternalID(charge_id)` → ранний выход `paid_duplicate`
    2. `MarkPaid` (UPDATE payments SET status='paid', external_id=charge_id)
    3. `sub.CreateSubscription(user_id, plan_id, max_devices)` (grpc)
    4. `vpn.CreateVPNUser(user_id, subscription_id)` (grpc → Xray AddUser на всех активных серверах)
  - **refunded_payment** → MarkRefunded + CancelSubscription + DisableVPNUser
- [x] ✅ **`vpn-service.DisableVPNUser`** — новый gRPC метод:
  RemoveUser во всех Xray inbound'ах (идемпотентно — "not found" игнорируется) + DELETE vpn_users (CASCADE чистит active_connections)
- [x] ✅ **Cron истечения подписок** в `subscription-service`:
  `service.ExpireCron` тикает каждые 10 минут, `UPDATE status='expired' WHERE expires_at < NOW() AND status='active' RETURNING user_id`, для каждого дёргает `vpn.DisableVPNUser` (subscription-service теперь имеет gRPC-клиент к vpn-service)
- [x] ✅ Gateway: `POST /api/v1/payments` + `GET /api/v1/payments` (обе под JWT) + `POST /api/v1/telegram/webhook` (публичная)
- [x] ✅ env + docker-compose: добавлен `payment-service`, `PAYMENT_TELEGRAM_WEBHOOK_SECRET`, `VPN_SERVICE_ADDR` для sub-service, gateway получает `TELEGRAM_WEBHOOK_SECRET`

**e2e проверено (все зелёные):**

```bash
# 1. Webhook без секрета → 403
curl -X POST /api/v1/telegram/webhook → 403 ✓

# 2. successful_payment webhook с payment_id=1, charge_id=TEST_CHARGE_123
curl -H "X-Telegram-Bot-Api-Secret-Token: $SECRET" ... → {"action":"paid","ok":true}
# БД: payments.status=paid, external_id=TEST_CHARGE_123, paid_at IS NOT NULL
# БД: subscriptions.status=active, expires_at > NOW()
# БД: vpn_users содержит user_id=1, email=user1@vpn.local
# Логи vpn-core: "xray user added" на server_id=5

# 3. Повторный webhook с тем же charge_id → {"action":"paid_duplicate","ok":true}
# БД не меняется, логи: "duplicate successful_payment, skipping" ✓

# 4. refunded_payment webhook → {"action":"refunded","ok":true}
# БД: payments.status=refunded, subscriptions.status=cancelled, vpn_users пустой
# Логи vpn-core: "xray user removed" + "VPN user disabled, servers_cleaned=1" ✓
```

**Что создано/изменено:**
```
shared/proto/
├── payment/v1/payment.proto                                  ← NEW
├── subscription/v1/subscription.proto                        ← +price_stars, +plan_name в DevicePrice
└── vpn/v1/vpn.proto                                          ← +DisableVPNUser rpc

platform/pkg/telegram/client.go                               ← NEW (Bot API минимальный клиент)

services/payment-service/                                     ← NEW (весь сервис ~600 строк)
├── cmd/main/main.go
├── Dockerfile
├── go.mod, go.sum
├── migrations/001_create_payments.{up,down}.sql
└── internal/
    ├── app/app.go                                            ← pgx + 2 gRPC client + grpc server + reflection
    ├── api/payment.go                                        ← 4 RPC handler'а
    ├── config/config.go                                      ← GRPC + DB + Services{subscription,vpn} + Telegram{BotToken,WebhookSecret}
    ├── model/payment.go                                      ← модель + status constants
    ├── repository/payment.go                                 ← CreatePending, MarkPaid (idempotent), GetByExternalID, MarkRefunded, ListByUser
    └── service/payment.go                                    ← CreateInvoice + HandleUpdate (pre_checkout / success / refund)

services/subscription-service/
├── migrations/002_add_stars_price.{up,down}.sql              ← +price_stars + seed 16 цен
├── internal/config/config.go                                 ← +Services.VPNAddr для cron
├── internal/model/subscription.go                            ← +PriceStars, +PlanName
├── internal/repository/subscription.go                       ← +ExpireOverdueSubscriptions + JOIN на plan_name
├── internal/service/expire_cron.go                           ← NEW (тикер 10мин)
├── internal/api/subscription.go                              ← +price_stars/plan_name в DevicePrice и SubscriptionPlan
└── internal/app/app.go                                       ← +vpn gRPC client + go a.expireCron.Run(ctx)

services/vpn-service/
├── internal/api/vpn.go                                       ← +DisableVPNUser handler
├── internal/service/vpn.go                                   ← +DisableVPNUser (RemoveUser во всех серверах + DeleteVPNUser)
└── internal/repository/vpn.go                                ← +DeleteVPNUser

services/gateway/
├── internal/config/config.go                                 ← +PaymentAddr, +TelegramConfig.WebhookSecret
├── internal/client/payment.go                                ← NEW
├── internal/handler/payment.go                               ← NEW (CreateInvoice + ListPayments + TelegramWebhook)
└── internal/app/app.go                                       ← +payment client, +payment routes, +webhook route (public)

deploy/
├── env/
│   ├── .env.template                                         ← +PAYMENT_TELEGRAM_WEBHOOK_SECRET
│   ├── payment.env.template                                  ← NEW
│   ├── sub.env.template                                      ← +VPN_SERVICE_ADDR
│   └── gateway.env.template                                  ← +TELEGRAM_WEBHOOK_SECRET, +PAYMENT_SERVICE_ADDR
└── compose/docker-compose.yml                                ← +payment-service, +migrate payment, +sub depends_on vpn, +gateway TELEGRAM_WEBHOOK_SECRET

docs/services/payment-integration.md                          ← NEW (полная дока + e2e reproducer + шпаргалка setup)
docs/services/README.md                                       ← +ссылка на payment-integration.md
Taskfile.yaml                                                 ← +build:payment, +dev:payment, SERVICES=auth,sub,vpn,payment,gateway
```

**Что осознанно НЕ сделано (TODO на будущее):**
- **Auto-fail pending cron** — если юзер закрыл Mini App не оплатив, payment остаётся `pending` навсегда. Надо cron в payment-service: `UPDATE status='failed' WHERE status='pending' AND created_at < NOW() - 1 hour`.
- **Transactional outbox** — если Gateway/DB упал между `MarkPaid` и `CreateSubscription` → payment.status=paid, но подписки нет. Сейчас исправляется вручную. Для 1.0 — outbox pattern с `background_jobs` таблицей.
- **Regist. webhook автоматом** — сейчас на проде нужно вручную дёрнуть `setWebhook`. Добавить в deploy/scripts/register-webhook.sh.
- **Integration тесты с Mini App** — реальный flow через тестовый режим Telegram Stars требует настоящий Telegram клиент. Сделаем на Этапе 11 closed beta.

---

### Этап 6 — Multi-server архитектура (1 день) ✅ ЗАВЕРШЁН (backend)

**Frontend часть** (UI списка серверов в Mini App) — в отдельном репо `vpn_next`, не в scope этой сессии. API готово: `GET /api/v1/vpn/servers` возвращает массив с `load_percent`, Mini App может строить UI с балансировщиком.

- [x] ✅ При `CreateVPNUser` — добавление UUID на ВСЕ активные серверы (реализовано в Этапе 2)
- [x] ✅ **Best-effort partial success** в `CreateVPNUser`: если один сервер недоступен — не роняем запрос, регистрируем юзера на остальных. Только если **все** серверы упали → ошибка. Лог содержит `servers_total/servers_ok/servers_failed`.
- [x] ✅ **Миграция 003** для `vpn_servers`: `+server_max_connections INT DEFAULT 1000`, `+description VARCHAR`
- [x] ✅ **LoadCron** (`services/vpn-service/internal/service/load_cron.go`): тикер 60с, для каждого активного сервера `UPDATE load_percent = COUNT(active_connections.last_seen > NOW()-5min) * 100 / server_max_connections` (clamped в `[0..100]`). Запускается в `app.Start()`, останавливается через closer.
- [x] ✅ **gRPC `ResyncServer(server_id)`**: `SELECT vpn_users` → `xray.AddUser` на inbound нового сервера. Идемпотентно (`already exists` считается success). Возвращает статистику `{total, added, already, failed}`. Нужен при добавлении 2-го/3-го VPS без простоя.
- [x] ✅ **Скрипт** `deploy/scripts/deploy-xray-new.sh`:
  - Генерирует свежие Reality x25519 keys + short_id (через docker)
  - Создаёт `config.json` для нового Xray в `deploy/compose/xray-new/<name>/`
  - Печатает пошаговую инструкцию: `scp` config, `docker run` на VPS, `INSERT INTO vpn_servers`, `ResyncServer` grpcurl

**e2e проверено (2 сервера):**

```bash
# 1. Стек: 1 дефолтный сервер (#5 "Local Xray (dev)", max=1000)
# 2. Создали user_id=1 + VPN user (UUID=6f58e664-...). Лог: servers_total=1, servers_ok=1

# 3. INSERT "Mock Server 2" (id=6, max=500, тот же inbound_tag для теста)
docker exec vpn-postgres psql ... INSERT INTO vpn_servers ... RETURNING id → 6

# 4. ResyncServer(6) → {usersTotal:1, usersAlready:1, usersFailed:0}
#    (юзер уже был — тот же inbound)

# 5. Второй юзер (user_id=2) через CreateVPNUser:
#    Лог: "VPN user created servers_total=2 servers_ok=2 servers_failed=0" ✓
#    Оба сервера прогнаны циклом.

# 6. GetVLESSLink на server_id=5 и на server_id=6:
#    active_connections: (vpn_user_id=1, server_id=5, iPhone) и (vpn_user_id=1, server_id=6, PC) ✓

# 7. После 60с load_cron: load_percent=0 (1 юзер из 1000 = 0 integer div)
#    После UPDATE server_max_connections=1 → load_percent=100 ✓
```

**Что создано/изменено:**
```
services/vpn-service/
├── migrations/003_add_server_capacity.{up,down}.sql         ← NEW
├── internal/model/vpn.go                                    ← +ServerMaxConnections, +Description
├── internal/repository/vpn.go                               ← UpdateServerLoad, ListActiveServerIDs, Scan
├── internal/service/load_cron.go                            ← NEW (тикер 60с)
├── internal/service/vpn.go                                  ← CreateVPNUser best-effort, ResyncServer
├── internal/api/vpn.go                                      ← +ResyncServer handler
└── internal/app/app.go                                      ← +loadCron, go a.loadCron.Run()

shared/proto/vpn/v1/vpn.proto                                ← +ResyncServer RPC

deploy/scripts/deploy-xray-new.sh                            ← NEW (генерация keys + config + SQL + resync)

docs/services/multi-server.md                                ← NEW (архитектура + e2e + инструкция)
docs/services/README.md                                      ← +ссылка на multi-server.md
```

**Что НЕ делали в рамках этапа 6:**
- UI в vpn_next (список серверов, selector, load_percent индикатор) — отдельно
- Auto-balancing (сервер с минимальным load_percent выбирается автоматически) — логика фронтенда, пока ручной выбор
- Параллельный `ResyncServer` для больших юзер-баз (сейчас последовательно) — не критично до 1000+ юзеров

---

### Этап 7 — Referral Service (1.5 дня)

**7.1 Инфра:**
- [ ] 🔵 **[СТОП, ПРОДОЛЖИТЬ ОТСЮДА]** Создать `services/referral-service/`, порт **50064**, префикс env `REFERRAL_*`
- [ ] Миграция:
  ```sql
  CREATE TABLE referral_links (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT UNIQUE NOT NULL REFERENCES users(id),   -- one user = one token
    token VARCHAR(16) UNIQUE NOT NULL,                      -- random 8-byte hex
    click_count INT DEFAULT 0,                              -- increments на каждый deep-link open
    created_at TIMESTAMPTZ DEFAULT NOW()
  );
  CREATE TABLE referral_bonuses (
    id BIGSERIAL PRIMARY KEY,
    inviter_id BIGINT NOT NULL REFERENCES users(id),
    invited_id BIGINT NOT NULL REFERENCES users(id),
    status VARCHAR(20) NOT NULL DEFAULT 'pending',          -- pending → rewarded → (no revoke)
    bonus_days INT NOT NULL DEFAULT 3,
    rewarded_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(invited_id)                                      -- каждый юзер — только 1 inviter
  );
  ```
- [ ] Proto: `GetOrCreateReferralLink(user_id)`, `RegisterReferral(inviter_token, invited_user_id)`, `GetReferralStats(user_id)`, `ApplyPendingBonus(invited_user_id)` (дёргается Payment Service'ом когда приглашённый купил первую подписку)

**7.2 Deep-link формат:**
- Telegram Mini App не поддерживает query-строки через браузер. Deep-link-формат:
  ```
  https://t.me/maydavpnbot?startapp=ref_a1b2c3d4
  ```
- Mini App получает это из `window.Telegram.WebApp.initDataUnsafe.start_param`, парсит префикс `ref_` → извлекает token → отдаёт в `POST /api/v1/auth/validate {init_data, ref_token}`.
- Auth Service: если юзер НОВЫЙ (первая регистрация) И `ref_token` валиден → вызов `referral-service.RegisterReferral(inviter_token, invited_user_id)`.

**7.3 Anti-abuse (критично):**
- [ ] **Self-invite:** `inviter.telegram_id != invited.telegram_id` — валидация в `RegisterReferral`
- [ ] **One inviter per invited:** `UNIQUE(invited_id)` в БД + проверка в коде
- [ ] **Existing user can't become "invited":** проверка `invited_user_created_at < 1 minute` — реферал считается только для только-что созданных юзеров
- [ ] **Rate limit на генерацию links:** один юзер — один токен навсегда (ON CONFLICT DO NOTHING), нельзя "перекрутить" 100 разных реф-ссылок
- [ ] **Bot-friendly token:** 8 байт hex (URL-safe, коротко, 2^64 коллизий пренебрежимо)

**7.4 State-machine бонусов:**
```
invited_user_id регистрируется с ref_TOKEN
  → INSERT referral_bonuses (status='pending')
  → Приглашённый сразу получает +3 дня к ПЕРВОЙ будущей подписке
    (хранится как pending discount в users.pending_bonus_days INT, списывается при CreateSubscription)

invited_user_id делает первую оплату (payment-service успех)
  → payment-service вызывает referral-service.ApplyPendingBonus(invited_id)
  → UPDATE referral_bonuses SET status='rewarded', rewarded_at=NOW()
  → Пригласитель получает +3 дня к АКТИВНОЙ подписке (subscription.expires_at += 3 дня)
    если активной подписки нет — бонус накапливается в users.pending_bonus_days для будущей
```

**7.5 API:**
- [ ] `GET /api/v1/referral/link` → `{url: "https://t.me/maydavpnbot?startapp=ref_a1b2c3d4", token: "a1b2c3d4"}`
- [ ] `GET /api/v1/referral/stats` → `{invited_count: 5, pending_count: 2, rewarded_days_total: 9, my_pending_days: 0}`
- [ ] Модификация Auth Service: `ValidateTelegramUserRequest.ref_token` (opt) + при создании нового юзера вызов `referral-service.RegisterReferral`
- [ ] Модификация Payment Service: после `successful_payment` → дёрнуть `referral-service.ApplyPendingBonus(user_id)`

**7.6 Frontend (в vpn_next):**
- [ ] Экран "Пригласить друга" с кнопкой "Поделиться" — вызов Telegram WebApp `shareStory` или копирование ссылки в clipboard
- [ ] Счётчик приглашённых + бонусных дней в профиле

**7.7 Follow-up (после первой реализации Этапа 7):**

Эти пункты появились по итогам интеграции referral-service со связкой
auth/payment/gateway/vpn_next и осознанно вынесены отдельно от MVP-объёма,
чтобы не блокировать запуск фичи. Делать сразу после прогона e2e (см. ниже)
или по отдельному запросу.

- [ ] **Unit-тесты на бизнес-логику** `services/referral-service/internal/service/referral_test.go`:
      - `RegisterReferral`: self-invite (inviter_telegram_id == invited.telegram_id) → 0-эффект,
      - freshness: invited.created_at старше `FRESHNESS_SECONDS` → не регистрируется,
      - повторный вызов с тем же `invited_id` → idempotent (UNIQUE relationships),
      - inviter.role='user' → +N дней обоим (через `subscription.AddDaysToActiveSubscription` или `users.pending_bonus_days`),
      - inviter.role='partner' → relationship создан, бонус ждёт ApplyBonus,
      - `ApplyBonus` идемпотентность: повторный вызов на ту же relationship не двоит начисление,
      - `CreateWithdrawalRequest`: insufficient_balance / not_partner / amount_too_small.
      Сейчас покрыт только token generator (`internal/token/`).

- [ ] **Lint cleanup в vpn_next** (вне scope Этапа 7, но накопилось 12 проблем):
      9 errors / 3 warnings — `react-hooks/set-state-in-effect`, `react-hooks/purity`,
      `@typescript-eslint/no-require-imports` (tailwind.config.ts).
      Файлы: `connect/page.tsx`, `history/page.tsx`, `auth-context.tsx`, `referral/page.tsx`, `tailwind.config.ts` и др.
      Можно сделать одним PR'ом с переходом на паттерн `useEffectEvent` / `cancellable AbortController`,
      затрагивает не только реферальную страницу.

- [ ] **Локальный e2e референтальной программы** (manual, не CI):
      1. `task compose:up` → миграции referral отрабатывают (видно в логах `migrate`).
      2. Юзер A: открыть Mini App → `GET /api/v1/referral/link` → токен `T`.
      3. Юзер B (новый Telegram-аккаунт): открыть `https://t.me/<bot>?startapp=ref_T` → запуск Mini App.
      4. Проверить в БД: `referral_relationships` с inviter_id=A, invited_id=B, status='registered';
         `referral_bonuses` две строки type='days'; у обоих active subscription продлена на 3 дня
         (или у B — `users.pending_bonus_days=3`, если активной не было — съедается следующей CreateSubscription).
      5. Если A.role='partner': B оплачивает любой план → проверить `users.balance` у A
         выросла на `amount * 0.30`, в `referral_bonuses` появилась строка type='balance', is_applied=true,
         relationship.status='purchased'.
      6. A создаёт `POST /api/v1/referral/withdrawal` → запись в `withdrawal_requests` со status='pending'.
      Требует реальный `AUTH_TELEGRAM_BOT_TOKEN` и тестовых TG-аккаунтов; в CI не гонится.

- [ ] **Admin UI обработки `withdrawal_requests`** — выносится в **Этап 8** (Admin Service).
      Сейчас юзер может создать заявку, но обработать её можно только ручным SQL.
      В Admin Service добавить:
      - `ListWithdrawalRequests(filter, limit, offset)` (proxy в referral-service),
      - `ApproveWithdrawalRequest(id, admin_comment)` → status='approved',
      - `RejectWithdrawalRequest(id, admin_comment)` → status='rejected' + откат `users.balance`,
      - `MarkWithdrawalPaid(id)` → status='paid' (после ручной выплаты вне системы).
      Соответствующие RPC уже есть в `services/referral-service/internal/api/referral.go` —
      админ-сервису остаётся только проксировать с проверкой `RequireRole("admin")`.

---

### Этап 8 — Admin Service (1 день)

**8.1 Инфра:**
- [ ] Создать `services/admin-service/`, порт **50065**, префикс env `ADMIN_*`
- [ ] Добавить в `platform/pkg/middleware/jwt.go` хелпер `RequireRole("admin")` — middleware поверх `JWTMiddleware`, проверяет `role=="admin"` из контекста. 403 `{"error":"forbidden"}` при несоответствии.
- [ ] Proto: `ListUsers(limit, offset, search)`, `GetUserDetails(user_id)`, `BanUser(user_id, is_banned)`, `ListSubscriptions(filter, limit, offset)`, `ListPayments(filter, limit, offset)`, `GetDashboardStats()` → `{total_users, active_subs, mrr_stars, today_revenue, today_new_users}`
- [ ] Роуты в Gateway: `r.Route("/api/v1/admin", func(r){ r.Use(jwtMw, RequireRole("admin")); ... })` — **двойная защита** (JWT + роль)

**8.2 Назначение первого админа (bootstrap):**
- [ ] В миграции auth-service сид — ничего не автоматизируем, aziz сам делает:
  ```sql
  UPDATE users SET role='admin' WHERE telegram_id = <aziz_tg_id>;
  ```
  После этого его текущий JWT невалиден (role изменилась) — нужен re-login.

**8.3 Пагинация + фильтры:**
- [ ] Все List-ручки обязательно с `limit INT (default 50, max 200)` и `offset INT`
- [ ] Search по users: `telegram_id`, `username`, `first_name` (LIKE %search%)
- [ ] Фильтры subscriptions: `status in (active, expired, refunded)`
- [ ] Фильтры payments: `status`, `date_from`, `date_to`

**8.4 Audit log (опционально, но желательно):**
- [ ] Таблица `admin_actions (id, admin_id, action, target_id, metadata, created_at)` — пишется при каждом `BanUser`, `UpdateRole` (Этап 8) или изменении подписки
- [ ] Видна в UI на странице каждого юзера → история модерации

**8.5 Frontend (vpn_next):**
- [ ] Простая админка — **отдельные роуты под JWT + role check на клиенте**:
  - `/admin/users` — таблица + search + "бан" кнопка
  - `/admin/subscriptions` — список с фильтрами по статусу
  - `/admin/payments` — список + `SUM(amount_stars)` за период
  - `/admin/stats` — карточки: MAU / MRR(Stars) / conversion / total users / today new users
- [ ] Для dev/prod одна кодовая база — просто role-based routing

---

### Этап 9 — Deployment + SSL (1-1.5 дня)

**Решение:** для MVP **1 VPS на весь стек** (backend + postgres + xray в одном docker-compose). Разделить на 2 VPS имеет смысл когда 100+ активных юзеров или если хочется изолировать Xray на случай компрометации backend'а — это не MVP-проблема.

**⚠️ Удалить из плана:** строка "Xray + Nginx (reverse proxy для Xray maskering)" из оригинального плана **ошибочна**. Reality сам маскируется под TLS-трафик (mimicry через `dest: github.com:443`). Nginx перед Xray не нужен и только добавит задержки.

**9.1 VPS setup:**
- [ ] Купить VPS: **Hetzner** (CX11 €4/мес), **Contabo** ($7/мес) или **PQ.hosting** (принимает рубли и Stars). Минимум: 2GB RAM, 20GB SSD, 1 vCPU, Ubuntu 24.04.
- [ ] `apt install docker.io docker-compose-v2` → добавить юзера в docker group
- [ ] `ufw allow ssh, 8443/tcp (VLESS)` — больше **ничего не открывать наружу**. 8081 gateway закрыт, доступен только через Cloudflare Tunnel.
- [ ] `git clone` репо → `cp deploy/env/.env.template deploy/env/.env` → заполнить prod-значения (сильный postgres-пароль, свежие Reality keys через `task xray:genkeys`, `AUTH_JWT_SECRET=$(openssl rand -base64 48)`, production bot token из `@BotFather`)
- [ ] `task compose:up` — стартует 6 контейнеров (postgres, migrate, auth, sub, vpn, gateway, xray)
- [ ] Миграции накатываются контейнером `migrate` при каждом `compose:up` (идемпотентно через `x-migrations-table`)

**9.2 Публичный доступ (⚠️ ПЕРЕСМОТРЕНО — см. [04-caddy-auto-tls.md](./04-caddy-auto-tls.md)):**

> 🔄 **2026-04-23:** Отказываемся от Cloudflare Tunnel в пользу прямого VPS + Caddy с автоматическим Let's Encrypt. Причины: DNS домена `api.osmonai.com` не на CF, возможность быстрой смены домена, VPN в серой зоне ToS CF. Полная спека — в [04-caddy-auto-tls.md](./04-caddy-auto-tls.md).

<details><summary>Оригинальный план CF Tunnel (устарело)</summary>
- [ ] Зарегистрировать бесплатный Cloudflare account, привязать домен (купить на namecheap/reg.ru — ~$10/год)
- [ ] `cloudflared tunnel login` → авторизация
- [ ] `cloudflared tunnel create vpn-maydavpn` — создаёт **именованный** туннель (в отличие от `trycloudflare.com` не меняет URL при рестарте)
- [ ] Создать `~/.cloudflared/config.yml`:
  ```yaml
  tunnel: <tunnel-id>
  credentials-file: /root/.cloudflared/<tunnel-id>.json
  ingress:
    - hostname: api.maydavpn.com
      service: http://localhost:8081
    - service: http_status:404
  ```
- [ ] DNS: `cloudflared tunnel route dns vpn-maydavpn api.maydavpn.com`
- [ ] Запуск как systemd service: `cloudflared service install`
- [ ] **Mini App URL** → `https://api.maydavpn.com` (через `@BotFather` → Mini App setup)

**9.3 Frontend деплой (vpn_next):**
- [ ] Вариант A (рекомендуется): **Vercel** — бесплатно, auto-deploy из GitHub. Set `NEXT_PUBLIC_API_URL=https://api.maydavpn.com` в Vercel env.
- [ ] Вариант B: на том же VPS в Docker, публикуется через тот же Cloudflare Tunnel на `app.maydavpn.com`.

**9.4 Webhook Telegram (после деплоя):**
- [ ] `curl -X POST 'https://api.telegram.org/bot$BOT_TOKEN/setWebhook' -d 'url=https://api.maydavpn.com/api/v1/telegram/webhook&secret_token=<random>'`
- [ ] Проверить `getWebhookInfo` — должен быть `pending_update_count: 0` и свежий URL

**9.5 Backup postgres (критично!):**
- [ ] Скрипт `deploy/scripts/backup.sh`:
  ```bash
  #!/bin/bash
  set -e
  DATE=$(date +%Y%m%d_%H%M%S)
  docker exec vpn-postgres pg_dump -U vpn vpn | gzip > /var/backups/vpn/db_$DATE.sql.gz
  # Ротация: держать 7 последних дневных + 4 недельных
  find /var/backups/vpn -name "db_*.sql.gz" -mtime +7 -delete
  # Upload в S3/R2/B2 (rclone)
  rclone copy /var/backups/vpn/db_$DATE.sql.gz r2:vpn-backups/
  ```
- [ ] Cron: `0 3 * * * /opt/vpn/backup.sh`
- [ ] Тест восстановления: `gunzip < db_YYYYMMDD.sql.gz | docker exec -i vpn-postgres psql -U vpn vpn`

**9.6 Secrets management:**
- [ ] `.env` файл лежит на VPS в `/opt/vpn/deploy/env/.env`, chmod 600, владелец root
- [ ] **Никогда не коммитить** в git (уже в .gitignore через `*.env` + `!*.env.template`)
- [ ] Для CI/CD деплоя (Этап 10) — GitHub Secrets или SOPS + age-encrypted файл в репе
- [ ] Reality private key + JWT_SECRET + postgres password — только в `.env`, не в docker image

**9.7 SSL / HTTPS (⚠️ ПЕРЕСМОТРЕНО — см. [04-caddy-auto-tls.md](./04-caddy-auto-tls.md)):**

> 🔄 **2026-04-23:** TLS termination делает Caddy внутри docker-compose (Let's Encrypt HTTP-01). Больше не Cloudflare edge. См. задачу 04.

**9.8 Post-deploy smoke test:**
- [ ] `curl https://api.maydavpn.com/health` → 200
- [ ] `curl https://api.maydavpn.com/api/v1/subscriptions/plans` → JSON
- [ ] Открыть Mini App в Telegram → авторизация → купить подписку (test mode Stars) → получить VLESS-ссылку → импортировать в Streisand (iOS) / v2rayNG (Android) → подключиться → `https://ipinfo.io` → IP = VPS

---

### Этап 10 — CI/CD + Мониторинг (1 день)

**10.1 CI (GitHub Actions):**
- [ ] Workflow `.github/workflows/ci.yml`:
  - На push в `master`: `task build-all` + `go test ./...` (тесты появятся по мере написания)
  - `task proto:gen` проверяет что генерация proto не даёт diff (чтобы не забыли коммитнуть)
- [ ] Workflow `.github/workflows/release.yml`:
  - На tag `v*` или manual dispatch: docker build всех сервисов → push в GHCR (`ghcr.io/<user>/vpn-auth:v1.2.3`)
  - Versioning через git tags

**10.2 Deploy:**
- [ ] Скрипт `deploy/scripts/deploy.sh` на VPS:
  ```bash
  cd /opt/vpn && git pull && docker compose -f deploy/compose/docker-compose.yml --env-file deploy/env/.env up -d --build
  ```
- [ ] GitHub Actions через SSH (deploy key) дёргает скрипт. Или проще — cron `*/5 * * * *` на VPS который `git pull && docker compose up -d` если есть изменения. Начать с cron, добавить CI-триггер позже.

**10.3 Healthchecks:**
- [ ] `/health` уже есть в gateway. Добавить **gRPC health reflection** в auth/sub/vpn/payment/referral/admin — `google.golang.org/grpc/health/v1`.
- [ ] В docker-compose добавить `healthcheck:` блок для каждого сервиса (через `grpc_health_probe` binary).

**10.4 Мониторинг:**
- [ ] **UptimeRobot** (бесплатно): ping `https://api.maydavpn.com/health` каждые 5 минут → SMS/Telegram на падении
- [ ] **Алерты в Telegram:** создать `@maydavpn_alerts_bot` отдельный — получает webhook'и от UptimeRobot и шлёт в приватный чат admin-у
- [ ] **Логи:** zap в stdout → `docker compose logs` → ротация через docker опцию `--log-opt max-size=100m max-file=5` (в compose уже не забыть)
- [ ] **Метрики (опционально):** Prometheus + Grafana в том же compose или Grafana Cloud (free tier). Пока не критично, добавим если понадобится.
- [ ] **Sentry (опционально):** для error tracking в Go — `github.com/getsentry/sentry-go`. На MVP можно пропустить.

---

### Этап 11 — Юридическое + Launch (0.5-1 день)

**11.1 Документы:**
- [ ] **Оферта** (публичный договор) — для MVP можно взять шаблон с https://tinkoff.ru или https://legalus.ru, адаптировать. Основные пункты:
  - Тип услуги (защищённый доступ в интернет, без логирования пользовательского трафика)
  - Порядок оплаты (через Telegram Stars non-refundable кроме случаев по правилам Telegram)
  - Отказ от ответственности за контент
  - Юрисдикция (куда покупатель может жаловаться)
  - Положить как статический файл в vpn_next `/public/offer.md` + страница `/offer`
- [ ] **Политика конфиденциальности** — `/privacy`:
  - Какие данные собираем: Telegram user_id, username, first_name, last_name, ip не логируем, payment история да
  - Cookies Mini App (минимум)
  - Срок хранения: пока подписка активна + 90 дней
  - Права субъекта: удалить аккаунт через @support
- [ ] Checkbox "Я согласен с офертой" перед первой оплатой (обязательный)

**11.2 Pre-launch checklist (за день до запуска):**
- [ ] `.env` переменные на VPS: JWT_SECRET ≥ 48 байт, POSTGRES_PASSWORD ≥ 20 байт, Reality keys свежие
- [ ] Cloudflare Tunnel работает стабильно 24+ часа
- [ ] Backup БД проверен (тест restore)
- [ ] Telegram webhook зарегистрирован, `pending_update_count = 0`
- [ ] Первый админ назначен (`users.role='admin'` для aziz)
- [ ] UptimeRobot мониторит `/health`
- [ ] Дежурный канал для алертов работает
- [ ] Выпущен первый git-тег `v0.1.0`

**11.3 Closed beta (friends):**
- [ ] Пригласить 3-5 человек (друзья) через реальные реф-ссылки
- [ ] Каждый проходит полный флоу: регистрация → оплата → подключение с iOS/Android/PC
- [ ] Собрать баги в Issues, починить **критичные** (flow-ломающие), остальные как `post-launch`

**11.4 Канал поддержки:**
- [ ] Отдельный бот `@maydavpn_support_bot` или аккаунт `@maydavpn_support` (обычный Telegram юзер-админ)
- [ ] В Mini App — кнопка "Написать в поддержку" → `t.me/maydavpn_support`
- [ ] Админ реагирует вручную (не автоматика). MVP ок.

**11.5 🚀 Публичный запуск:**
- [ ] Пост в личных каналах / чатах aziz
- [ ] Опционально: ProductHunt, TGstats, список VPN-ботов
- [ ] Мониторинг первых 48 часов: UptimeRobot + `docker compose logs -f | grep -iE 'error|panic'`
- [ ] Готовность к rollback: знать команду `git checkout v0.1.0 && task compose:up`

---

## 📋 Суммарная таблица усилий

| Этап | Оценка | Факт | Статус | Риски |
|---|:---:|:---:|:---:|---|
| 0. Инфра/решения | — | — | ✅ | — |
| 1. Фундамент | 1-2 дня | 1 сессия | ✅ | Сняты |
| 2. Xray | 2-3 дня | 1 сессия | ✅ | Сняты (Reality работает) |
| 3. Device limit | 0.5 дня | 1 сессия | ✅ | Сняты |
| 4. JWT middleware | 0.5 дня | 1 сессия | ✅ | Сняты |
| 5. Payment 🔴 | 2-3 дня | 1 сессия | ✅ | Сняты (idempotency через UNIQUE external_id, refund через DisableVPNUser) |
| 6. Multi-server | 1 день | 1 сессия | ✅ (backend) | Сняты (best-effort partial success + ResyncServer + LoadCron + deploy script) |
| 7. Referral | 1.5 дня | — | ⬜ | Anti-abuse (self-invite, double-dip) |
| 8. Admin | 1 день | — | ⬜ | Role bootstrap + audit log |
| 9. Deploy+SSL | 1-1.5 дня | — | ⬜ | Cloudflare Tunnel стабильность, backup |
| 10. CI/CD + Monitoring | 1 день | — | ⬜ | Низкие |
| 11. Юридика/Launch | 0.5-1 день | — | ⬜ | Оферта-шаблон подходит под Stars? |
| **Осталось** | **4-6 дней** | — | | |

---

## ⚠️ Риски и смягчения

### Закрытые (исполненные этапы)
| Риск | Исход |
|---|---|
| ~~Xray Reality handshake ломается~~ | ✅ Работает (e2e в Этапе 2: curl → SOCKS5 → Reality → ipinfo.io) |
| ~~ЮKassa не одобрит магазин~~ | ✅ Выбрали Telegram Stars, этот риск снят |
| ~~Reality-ключи утекут через git~~ | ✅ `.gitignore` с `*.env + !*.env.template` + `deploy/compose/xray/config.json` gitignored |
| ~~Подмена `?user_id=99` в query-параметре~~ | ✅ JWT middleware (Этап 4), query `user_id` игнорируется |
| ~~Telegram дублирует webhook → двойное начисление~~ | ✅ UNIQUE(external_id) + idempotent handler (Этап 5). e2e проверено: 2 раза `paid_duplicate`, БД не меняется |
| ~~Refund → юзер остался с активным VPN~~ | ✅ `refunded_payment` → DisableVPNUser → RemoveUser из Xray + DELETE vpn_users. e2e проверено |
| ~~Истекшая подписка → юзер продолжает пользоваться VPN~~ | ✅ ExpireCron (10мин) → UPDATE status='expired' + DisableVPNUser |
| ~~Один Xray VPS упал → все юзеры без VPN~~ | ✅ Multi-server готов: CreateVPNUser регистрирует на всех доступных (best-effort partial success), UI выбирает сервер с минимальным load_percent |
| ~~Добавление нового сервера требует простоя~~ | ✅ `vpn-service.ResyncServer(id)` прописывает существующих юзеров без даунтайма + `deploy-xray-new.sh` |

### Актуальные на оставшиеся этапы
| Риск | Вероятность | Удар | Смягчение |
|---|---|---|---|
| Telegram дублирует webhook → двойное начисление подписки | Высокая | Высокий | **UNIQUE `payments.external_id`** + idempotent handler (Этап 5.3) |
| Gateway упал во время `successful_payment` → Telegram ретраит 30 мин, но у нас нет обработчика | Низкая | Высокий | Healthcheck + auto-restart docker + UptimeRobot (Этап 10) |
| Cloudflare Tunnel теряет connection → Mini App недоступно | Низкая | Высокий | Named tunnel + systemd restart; fallback — вернуться на nginx+certbot если Cloudflare стабильно падает |
| Xray рестартует → все VLESS users в RAM потеряны | Средняя | Высокий | **Re-seed при старте vpn-service** (🔴 TODO, не закрыт — см. Cross-cutting) |
| БД `postgres` повреждена / потеряна | Низкая | Критический | **pg_dump в cron + rclone в S3/R2** (Этап 9.5) + тест restore |
| Reality private key утечёт (dev key в git) | Средняя | Высокий | **Сменить все ключи на проде** через `task xray:genkeys` (Этап 9.1); dev-ключи остаются в .env.template |
| Referral abuse (один юзер регает 100 фейк-аккаунтов) | Высокая | Средний | Anti-abuse в 7.3 + ручной контроль в админке |
| Telegram banит бота (VPN серая зона) | Средняя | Критический | ToS говорят "nope VPN не запрещён". Бэкап-план: иметь второй бот + inactive copy данных |
| Rate-limit DDoS на `/auth/validate` | Средняя | Средний | **Rate limiting chi middleware** в Gateway (добавить в Этап 4 ex-post или 10): `github.com/go-chi/httprate` 100 req/min per IP |
| Юзер жалуется в Telegram Support → возврат денег → у нас не удалён из Xray | Низкая | Средний | `refunded_payment` webhook → `vpn-service.DisableVPNUser` (Этап 5.4) |

### Cross-cutting TODOs (не привязаны к этапам, надо держать в голове)
- **Rate limiting** в Gateway — добавить хотя бы на `/auth/validate` и `/payments` до Этапа 9
- **Re-seed Xray при рестарте** 🔴 — в `services/vpn-service/internal/app/app.go::initXray` (или отдельным шагом после него) добавить: `SELECT vpn_users JOIN vpn_servers WHERE is_active` → для каждой пары `(server.inbound_tag, user.uuid, user.email, user.flow)` вызвать `xray.AddUser`. Идемпотентно: ошибку "already exists" от Xray игнорируем. **Acceptance:** `docker compose restart xray && sleep 3 && <клиент с существующим UUID получает соединение>` — сейчас: connection reset. Блокер для прода: любой рестарт Xray (обновление, OOM, `task xray:restart`) отключает всех платных клиентов до ручного re-`CreateVPNUser`. Привязка к Этапу 6 оказалась ошибочной — Этап 6 ✅, а пункт не сделан.
- **Graceful shutdown xray** — дать 30с на закрытие VLESS-соединений
- **Ротация Reality keys** — как поменять без простоя? Документ-gist: добавить новый short_id, дать время клиентам обновить ссылку, потом удалить старый
- **Бан юзера через middleware** — сейчас `users.is_banned=true` выставляется, но токен продолжает работать. Надо в JWT middleware добавить проверку `SELECT is_banned FROM users WHERE id=?` (или bloom-filter cached)

---

## 🧱 Что нужно от aziz для старта

См. **[Этап 0](#этап-0--инфра-и-решения-до-старта-кодинга)**. Конкретные вопросы — в ответе ассистента.

---

## 📎 Ссылки

- [01-mvp-plan.md](./01-mvp-plan.md) — обоснование выбора Варианта C
- [ARCHITECTURE.md](../ARCHITECTURE.md) — архитектурная схема
- [specs/](../specs/) — детальные спеки сервисов
