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

### Этап 3 — Device Limit (0.5 дня)
- [ ] 🔵 **[СТОП, ПРОДОЛЖИТЬ ОТСЮДА]** В `active_connections` использовать `last_seen` как heartbeat
- [ ] Фоновая задача (каждые 60с): опрос Xray Stats API → обновление `last_seen` для активных UUID-ов
- [ ] В момент `GetVLESSLink`: проверка `COUNT(active_connections WHERE last_seen > NOW() - 5min) < max_devices`
- [ ] Если превышен — вернуть ошибку "device limit exceeded"
- [ ] Метод `DisconnectDevice` для ручной очистки (юзер в UI нажал "отключить")

---

### Этап 4 — Auth Middleware в Gateway (0.5 дня)
- [ ] Добавить JWT middleware `platform/pkg/grpc/jwt_middleware.go`
- [ ] В Gateway все `/api/v1/*` (кроме `/auth/validate`, `/health`, `/subscriptions/plans`) → требуют JWT
- [ ] JWT приходит в `Authorization: Bearer <token>`
- [ ] В контекст запроса кладётся `userId` — хендлеры берут оттуда, не из payload
- [ ] Auth Service выдаёт JWT в ответ `ValidateTelegramUser`

---

### Этап 5 — Payment Service (2-3 дня) 🔴 Критический
Зависит от выбора: Telegram Stars / ЮKassa.

**5.1 Общее:**
- [ ] Создать `services/payment-service/` по шаблону других
- [ ] Порт: **50053**
- [ ] Proto: `CreatePayment`, `GetPaymentStatus`, `HandleWebhook`, `ListUserPayments`
- [ ] Таблица `payments` (уже есть в `schema.sql`, накатить миграцию)

**5.2 Telegram Stars (выбран):**
- [ ] Интеграция с Bot API `sendInvoice` (`currency: "XTR"`)
- [ ] Нужен отдельный небольшой сервис (или endpoint в payment-service) для общения с Telegram Bot API
- [ ] Webhook от Telegram: `pre_checkout_query` → подтверждаем, потом `successful_payment` → активация
- [ ] В боте (`@maydavpnbot`) настроить обработчики `pre_checkout_query` + `successful_payment`
- [ ] Цены задаём в Stars (1 Star ≈ 0.013$ — но это для статистики, в UI показываем ⭐)
- [ ] Пример: 1 мес × 1 устройство = 100⭐, 12 мес × 3 устройства = 1500⭐

**5.3 Общий флоу:**
- [ ] `POST /api/v1/payments` → создаёт платёж, возвращает URL/invoice
- [ ] Webhook → Payment Service проверяет подпись → меняет статус в БД → дёргает Subscription Service (`CreateSubscription`) → дёргает VPN Service (`CreateVPNUser`)
- [ ] Атомарность: если один шаг упал — откат или retry через outbox pattern
- [ ] Все платежи логируются в `payments` таблицу

---

### Этап 6 — Multi-server архитектура (1 день, 1 сервер на старте)

**Важно:** На MVP разворачиваем **только 1 Xray VPS**, но архитектура готова к multi-server. Добавление второго/третьего — отдельная задача после первых продаж.

- [ ] При `CreateVPNUser` — добавлять UUID на ВСЕ активные серверы из `vpn_servers` (на старте 1, но код не должен предполагать 1)
- [ ] В UI фронта: показывать список серверов, юзер нажимает → получает ссылку на конкретный сервер
- [ ] Cron health-check каждые 60с → `load_percent` обновляется по `active_connections`
- [ ] Тест: добавить в БД фейковую 2-ю строку — убедиться что код не падает
- [ ] Подготовить Ansible/скрипт для быстрого развёртывания Xray на новой VPS (чтобы в будущем `./deploy-xray.sh новый.сервер` было достаточно)

---

### Этап 7 — Referral Service (1.5 дня)
- [ ] Создать `services/referral-service/`, порт **50054**
- [ ] Миграция: таблицы `referral_links`, `referral_bonuses`
- [ ] Proto: `CreateReferralLink`, `TrackClick`, `RegisterReferral`, `GetReferralStats`
- [ ] При регистрации нового юзера с `?ref=TOKEN`:
  - +3 дня к первой подписке приглашённому
  - +3 дня пригласителю (отложенно — когда приглашённый купит подписку)
- [ ] Эндпоинт `GET /api/v1/referral/link` → возвращает уникальную ссылку юзера
- [ ] Эндпоинт `GET /api/v1/referral/stats` → кол-во приглашённых, заработанные дни
- [ ] UI экран "Пригласить друга" во фронте

---

### Этап 8 — Admin Service (1 день)
- [ ] Создать `services/admin-service/`, порт **50055**
- [ ] Middleware: требует JWT с `role=admin`
- [ ] Proto: `ListUsers`, `GetUserDetails`, `BanUser`, `ListSubscriptions`, `ListPayments`, `GetDashboardStats`
- [ ] Роуты в Gateway: `/api/v1/admin/*` (под JWT+role проверкой)
- [ ] Простая админка в `vpn_next`:
  - `/admin/users` — таблица юзеров, фильтры, кнопка "бан"
  - `/admin/subscriptions` — список подписок
  - `/admin/payments` — список платежей + сумма выручки
  - `/admin/stats` — dashboard (MAU, MRR, conversion)

---

### Этап 9 — Deployment + SSL (1 день)
- [ ] `docker-compose.yml` для всего стека (backend + postgres) на backend VPS
- [ ] Xray + Nginx (reverse proxy для Xray maskering) на Xray VPS — отдельный docker-compose
- [ ] **Cloudflare Tunnel** на backend VPS вместо nginx/SSL (проще, быстрее, бесплатно):
  - `cloudflared tunnel` на backend VPS → публикует gateway (`:8081`) как `https://*.trycloudflare.com` или (позже) через именованный туннель на свой домен
  - Mini App URL — этот туннель
- [ ] Next.js Mini App задеплоить: **Vercel** (бесплатно, CI/CD из коробки) или на backend VPS через Cloudflare tunnel
- [ ] Настроить Mini App URL в `@BotFather`
- [ ] Secrets через `.env` файл (не в git!), в проде через docker secrets или CI vault
- [ ] **TODO на будущее:** переехать с Cloudflare trycloudflare.com на свой домен (простая операция, но не срочно)

---

### Этап 10 — CI/CD + Мониторинг (1 день)
- [ ] GitHub Actions: на push в `master` → build всех сервисов → push docker images в GHCR
- [ ] Автодеплой: CI ssh в прод VPS → `docker-compose pull && up -d`
- [ ] Healthcheck endpoints на всех сервисах (`/health` — есть в gateway, добавить в остальные)
- [ ] Uptime-мониторинг (UptimeRobot / BetterStack) пинг `/health`
- [ ] Алерты в Telegram (свой бот в приватный чат админа)
- [ ] Логи собираются в `logs/` с ротацией

---

### Этап 11 — Юридическое + Launch (0.5-1 день)
- [ ] Написать **оферту** (публичный договор) и положить на `/offer`
- [ ] Написать **политику конфиденциальности** — `/privacy`
- [ ] Добавить ссылки на оферту/политику в UI (checkbox при первой покупке)
- [ ] Создать тестового юзера (не себя) и пройти полный флоу
- [ ] Протестировать с 3-5 реальными юзерами (друзья) — собрать баги
- [ ] Починить критичные баги
- [ ] 🚀 **Публичный запуск**

---

## 📋 Суммарная таблица усилий

| Этап | Дней | Блокер | Риски |
|---|:---:|:---:|---|
| 0. Инфра/решения | — | 🔴 Старт | Долго покупать VPS/ИП |
| 1. Фундамент | 1 | | Низкие |
| 2. Xray | 2-3 | 🔴 | Reality капризный |
| 3. Device limit | 0.5 | | Низкие |
| 4. JWT middleware | 0.5 | | Низкие |
| 5. Payment | 2-3 | 🔴 | Webhook/idempotency |
| 6. Multi-server | 1 | | 2 доп. VPS |
| 7. Referral | 1.5 | | Низкие |
| 8. Admin | 1 | | Низкие |
| 9. Deploy+SSL | 1 | | DNS проблемы |
| 10. CI/CD | 1 | | Низкие |
| 11. Юридика/Launch | 1 | | Юрист? |
| **Итого** | **12-14 дней** | | |

---

## ⚠️ Риски и смягчения

| Риск | Вероятность | Удар | Смягчение |
|---|---|---|---|
| Xray Reality handshake ломается при определённых клиентах | Высокая | Средний | Тестировать на Android/iOS/PC заранее, иметь fallback на обычный VLESS без Reality |
| ЮKassa не одобрит магазин (VPN серая зона в РФ) | Высокая | Высокий | Запасной вариант Telegram Stars |
| Reality-ключи утекут → все юзеры разлогинены | Низкая | Высокий | Не коммитить ключи, хранить в `.env`/secrets |
| Пользователей больше чем max_connections у Xray | Низкая | Средний | Мониторинг load_percent, автоматический ротатор |
| Атака на API (бруто JWT/spamm webhook) | Средняя | Средний | Rate limiting в Gateway (chi middleware есть) |

---

## 🧱 Что нужно от aziz для старта

См. **[Этап 0](#этап-0--инфра-и-решения-до-старта-кодинга)**. Конкретные вопросы — в ответе ассистента.

---

## 📎 Ссылки

- [01-mvp-plan.md](./01-mvp-plan.md) — обоснование выбора Варианта C
- [ARCHITECTURE.md](../ARCHITECTURE.md) — архитектурная схема
- [specs/](../specs/) — детальные спеки сервисов
