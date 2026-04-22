# Auth Middleware (JWT) — защита API

Как работает авторизация в Gateway: логин через Telegram initData → JWT-токен → middleware проверяет его на каждом защищённом запросе.

**Статус реализации:** ✅ **реализовано** (Этап 4 MVP, 2026-04-22)

---

## 🤔 Зачем это нужно

Сейчас в Gateway ручки выглядят так:

```go
userID := int64(1) // TODO: из JWT token
if v := r.URL.Query().Get("user_id"); v != "" { ... }  // query для тестов
```

То есть кто угодно может послать `?user_id=5` и получить VPN-ссылку чужого юзера. **Полная дыра.**

Задача Этапа 4 — закрыть дыру: `user_id` приходит из **подписанного токена**, который нельзя подделать.

---

## 🎟️ Аналогия — «браслет в отеле»

```
🏨 Отель (наш API)
👤 Гость (юзер) заселяется → на ресепшене показывает Telegram initData
🎫 Ресепшн (Auth Service) выдаёт браслет с серийным номером
   = JWT-токен: "user_id=1, выдан до 2026-04-29, подпись=xxxx"
🚪 Каждая дверь в отеле (ручки API) проверяет браслет
   — подпись настоящая?
   — срок не истёк?
   — user_id прочитан из браслета, а не из пальца показанного
```

Подделать браслет нельзя — у него **криптографическая подпись** секретом, который знают только Auth Service и Gateway.

---

## 🔐 JWT — что это

**JWT (JSON Web Token)** — строка из 3 частей через точки:

```
eyJhbGciOiJIUzI1NiJ9.eyJ1c2VyX2lkIjoxLCJleHAiOjE3MzAwfQ.abc123signatureXYZ
└────── header ─────┘└───── payload (данные) ─────┘└────── подпись ─────┘
```

- **header** — `{"alg":"HS256"}` — алгоритм подписи
- **payload** — `{"user_id":1, "exp":1730000000, "role":"user"}` — данные юзера
- **signature** — `HMAC-SHA256(base64(header) + "." + base64(payload), JWT_SECRET)`

**Магия:** любой может прочитать payload (это base64, а не шифрование). Но чтобы изменить `user_id=1` → `user_id=99`, надо пересчитать подпись, а для этого нужен `JWT_SECRET`, известный только серверу. Без секрета подделка не пройдёт проверку.

---

## 🏗️ Поток «логин → защищённая ручка»

```
┌──────────────────────────────────────────────────────────────────────┐
│  1. Первый вход в Mini App (публичная ручка)                         │
│                                                                       │
│  📱 Mini App                                                          │
│      ↓ POST /api/v1/auth/validate                                     │
│      ↓   Body: { init_data: "query_id=...&user=...&hash=..." }       │
│      ↓   (Telegram-подписанный payload; проверяется bot token)        │
│                                                                       │
│  ⚙️  Gateway → Auth Service (gRPC)                                    │
│      ├─ HMAC-проверка initData против TELEGRAM_BOT_TOKEN              │
│      ├─ INSERT/UPDATE users                                           │
│      └─ Генерирует JWT:                                               │
│         { user_id: 1, role: "user", exp: NOW + 168h }                 │
│         signed = HMAC-SHA256(payload, JWT_SECRET)                     │
│                                                                       │
│  📱 ← { "token": "eyJ...", "user": {...} }                            │
│      — хранит токен в localStorage                                    │
└──────────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────────┐
│  2. Любой последующий запрос (защищённая ручка)                      │
│                                                                       │
│  📱 GET /api/v1/vpn/servers/5/link?device_id=iPhone                   │
│     Header: Authorization: Bearer eyJ...                              │
│                                                                       │
│  ⚙️  Gateway JWTMiddleware                                            │
│      ├─ Нет Authorization header           → 401                      │
│      ├─ Не начинается с "Bearer "          → 401                      │
│      ├─ Неверная подпись (секрет не тот)   → 401                      │
│      ├─ Срок истёк (exp < now)             → 401                      │
│      └─ OK: кладёт user_id=1 в context.Context                        │
│                                                                       │
│  📁 Handler (vpn.go)                                                  │
│      userID := mw.UserIDFromContext(r.Context())                      │
│      // нет больше query-параметра user_id, нет TODO                  │
│      resp := vpnClient.GetVLESSLink(ctx, userID, serverID, deviceID)  │
│                                                                       │
│  ← vless://...                                                        │
└──────────────────────────────────────────────────────────────────────┘
```

---

## 🚪 Какие ручки защищаем, а какие нет

**Публичные (без JWT):**
- `GET /health` — health check
- `POST /api/v1/auth/validate` — сам логин (откуда иначе брать токен?)
- `GET /api/v1/subscriptions/plans` — прайс-лист (для неавторизованных Mini App)

**Защищённые (нужен `Authorization: Bearer <jwt>`):**
- `GET /api/v1/vpn/servers` и `:id/link`
- `GET /api/v1/vpn/connections`
- `DELETE /api/v1/vpn/devices/:id`
- `POST /api/v1/subscriptions` (купить)
- `GET /api/v1/subscriptions/active`
- `GET /api/v1/subscriptions/history`
- (будущие) `/api/v1/payments`, `/api/v1/referral/*`
- Admin-ручки будут защищены **и** JWT, **и** доп. проверкой `role == "admin"` (Этап 8)

---

## 🔑 Где хранится секрет

`JWT_SECRET` — одна и та же строка в двух местах:

1. **Auth Service** — использует для **подписи** токенов
2. **Gateway** — использует для **проверки** токенов

В мастер-env `deploy/env/.env.template` это **одна переменная** — `AUTH_JWT_SECRET`. Per-service шаблоны маппят её:

```
# deploy/env/.env.template
AUTH_JWT_SECRET=change_me_to_long_random_string_min_32_chars

# deploy/env/auth.env.template
JWT_SECRET=${AUTH_JWT_SECRET}

# deploy/env/gateway.env.template
JWT_SECRET=${AUTH_JWT_SECRET}
```

Это исключает drift: поменял в мастере → `task env:generate` → оба сервиса получают обновлённый секрет.

---

## 🎬 Сценарии безопасности

### ✅ Легальный поток
```
Mini App → /auth/validate → JWT получен (TTL 7 дней)
Mini App → /vpn/link (Authorization: Bearer eyJ...) → 200
```

### ❌ Без токена
```
curl /api/v1/vpn/servers/5/link?device_id=iPhone
   → 401 {"error":"missing_token"}
```

### ❌ С фейковым токеном
Злоумышленник подделал payload с `user_id=99`, но не знает `JWT_SECRET`:
```
curl -H "Authorization: Bearer eyJhbGci...FAKE..." ...
   → 401 {"error":"invalid_signature"}
```

### ❌ С истёкшим
Токен выдан 8 дней назад, `exp < now()`:
```
   → 401 {"error":"token_expired"}
```
Mini App ловит 401 → редиректит на `/auth/validate` за новым токеном.

### ❌ `?user_id=99` в query
После рефакторинга query-параметр `user_id` **игнорируется** в защищённых ручках. Единственный источник user_id — подписанный токен.

---

## 📋 Где это в коде

| Файл | Что делает |
|---|---|
| [`platform/pkg/middleware/jwt.go`](../../platform/pkg/middleware/jwt.go) | `JWTMiddleware(secret)` — парсит `Authorization: Bearer …`, проверяет HS256-подпись и exp, кладёт `userID`/`role` в context. Различает `missing_token` / `invalid_scheme` / `invalid_signature` / `token_expired` → 401 с JSON `{error, message}`. Хелперы `UserIDFromContext(ctx)` и `RoleFromContext(ctx)`. |
| [`services/auth-service/internal/service/auth.go`](../../services/auth-service/internal/service/auth.go) | `GenerateJWT(userID, role)` + `VerifyJWT(token)` на `github.com/golang-jwt/jwt/v5`. `ValidateTelegramUser` возвращает `(user, jwt_token)` → в ответе клиенту. |
| [`services/gateway/internal/config/config.go`](../../services/gateway/internal/config/config.go) | `JWTConfig.Secret` + `Validate()` — сервис не стартует без `JWT_SECRET`. |
| [`services/gateway/internal/app/app.go`](../../services/gateway/internal/app/app.go) | `authmw.JWTMiddleware(cfg.JWT.Secret)` применяется через `r.Group(func(r) { r.Use(jwtMw); … })`. Публичные ручки (`/auth/validate`, `/subscriptions/plans`, `/health`) — без middleware. |
| [`services/gateway/internal/handler/context.go`](../../services/gateway/internal/handler/context.go) | Хелпер `userIDFromRequest(w, r) (int64, bool)` — достаёт user_id из context или пишет 401 `unauthorized`. |
| `services/gateway/internal/handler/vpn.go` + `subscription.go` | Убраны все `userID := int64(1)` и query-parameter `user_id`. Теперь везде `userID, ok := userIDFromRequest(w, r); if !ok { return }`. |
| `deploy/env/.env.template` | `AUTH_JWT_SECRET=…` (мастер). |
| `deploy/env/gateway.env.template` | `JWT_SECRET=${AUTH_JWT_SECRET}` — один и тот же секрет для подписи (auth) и проверки (gateway). |
| `deploy/compose/docker-compose.yml` | В env gateway-контейнера прокидывается `JWT_SECRET: ${AUTH_JWT_SECRET}`. |
| [`platform/go.mod`](../../platform/go.mod) | `github.com/golang-jwt/jwt/v5 v5.2.1` (impl middleware). |

---

## 🎯 Итог одной фразой

> **JWT Middleware = секьюрити-щит на Gateway. Юзер логинится через Telegram → получает подписанный токен → на каждом запросе токен проверяется, и `user_id` берётся оттуда. Без валидного токена — 401.**

---

## 📚 См. также

- [xray-integration.md](./xray-integration.md) — как устроен VPN Service
- [device-limit.md](./device-limit.md) — лимит устройств (защищается тем же JWT)
- [01-mvp-plan.md](../tasks/01-mvp-plan.md) — план MVP
- [02-mvp-c-implementation.md](../tasks/02-mvp-c-implementation.md) — прогресс
- [JWT RFC 7519](https://datatracker.ietf.org/doc/html/rfc7519)
