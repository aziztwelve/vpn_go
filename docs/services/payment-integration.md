# Payment Service — оплата через Telegram Stars

Как работает платёжный флоу: юзер покупает подписку → оплачивает в Telegram → webhook → подписка активна → VPN-юзер создан.

**Статус реализации:** ✅ **реализовано** (Этап 5 MVP, 2026-04-22)

---

## 🧠 Простыми словами

У нас **нет своей платёжной системы**. Всё делает Telegram:
1. Мы создаём **invoice link** (по сути "счёт") через Bot API
2. Юзер открывает его в Mini App, платит Stars через встроенный UI Telegram
3. Telegram шлёт нам **webhook** что оплата прошла
4. Мы верим Telegram (он подписал webhook shared-секретом) и активируем подписку

Это **не PCI-DSS scope** — у нас нет данных карт, мы не работаем с деньгами напрямую. Stars — внутренняя валюта Telegram, они сами занимаются выплатами разработчикам.

---

## 🏗️ Архитектура

```
┌─────────────────────────────────────────────────────────────────────────┐
│  📱 Mini App                                                             │
│                                                                          │
│  1. "Купить подписку" → POST /api/v1/payments                            │
│     body: {plan_id: 1, max_devices: 2}                                   │
│     Authorization: Bearer <JWT>                                          │
│                         │                                                │
│  2. Получает invoice_link     ◄──────┐                                   │
│     → WebApp.openInvoice(link)       │                                   │
│                                       │                                  │
│  3. Юзер платит Stars во встроенном UI Telegram                          │
│                                                                          │
│  4. WebApp event "successfulPayment" → UI показывает VLESS-ссылку        │
└──────────────────────────────────────┼──────────────────────────────────┘
                                       │
                                       │ HTTP POST
                                       ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  ⚙️  Gateway                                                             │
│                                                                          │
│  POST /api/v1/payments (защищено JWT)    ──┐                             │
│     → call payment-service.CreateInvoice   │                             │
│                                             │                             │
│  POST /api/v1/telegram/webhook (публично,   │                             │
│    но header X-Telegram-Bot-Api-Secret-Token проверяется)  ──┐           │
│     → call payment-service.HandleTelegramUpdate              │           │
└──────────────────────────────┼───────────────────────────────┼──────────┘
                               │ gRPC                           │ gRPC
                               ▼                                ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  💳 Payment Service (:50063)                                             │
│                                                                          │
│  CreateInvoice:                                                          │
│    1. sub.GetDevicePricing(plan_id) → price_stars                        │
│    2. INSERT payments (status='pending')                                 │
│    3. tg.createInvoiceLink(payload=payment_id) → t.me/$…                 │
│                                                                          │
│  HandleUpdate (pre_checkout_query):                                      │
│    → tg.answerPreCheckoutQuery(ok=true)                                  │
│                                                                          │
│  HandleUpdate (successful_payment):                                      │
│    1. DEDUP: SELECT payments WHERE external_id=charge_id → skip if found │
│    2. UPDATE payments SET status='paid', external_id=charge_id           │
│    3. sub.CreateSubscription(user_id, plan_id, max_devices)              │
│    4. vpn.CreateVPNUser(user_id, subscription_id)                        │
│                                                                          │
│  HandleUpdate (refunded_payment):                                        │
│    1. UPDATE payments SET status='refunded'                              │
│    2. sub.GetActiveSubscription + sub.CancelSubscription                 │
│    3. vpn.DisableVPNUser (RemoveUser из Xray + DELETE vpn_users)         │
└──────────────────────────────┬──────────────────────────────────────────┘
                               │ HTTP → api.telegram.org
                               ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  🤖 Telegram Bot API                                                     │
│                                                                          │
│  createInvoiceLink    →  возвращает t.me/$… ссылку                       │
│  answerPreCheckoutQuery                                                  │
│  refundStarPayment (не вызываем, only admin)                             │
│                                                                          │
│  ← webhook POST с update (pre_checkout_query / successful_payment /     │
│    refunded_payment) на URL из setWebhook                                │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 🎟️ Аналогия — «билет в театр через Telegram»

```
Ты хочешь билет (подписку):
  1. Касса (наш Gateway) печатает чек (invoice_link)
  2. Telegram — это банк-эквайер
  3. Ты оплачиваешь в Telegram, Telegram списывает Stars
  4. Telegram говорит кассе "оплачено" (webhook)
  5. Касса выдаёт билет (VLESS-ссылку)

Возврат (refund):
  Ты жалуешься в Telegram Support → Telegram возвращает Stars
  → Telegram говорит кассе "отмена" (refunded_payment webhook)
  → Касса аннулирует билет (DisableVPNUser)
```

Мы **никогда** не видим карты, счетов, никакого PCI-DSS scope.

---

## 🗂️ Данные

### `payments` таблица
| Колонка | Что |
|---|---|
| `id` | PK |
| `user_id` | FK users |
| `plan_id` | FK subscription_plans |
| `max_devices` | что покупает юзер |
| `amount_stars` | цена в Stars |
| `status` | `pending` → `paid` / `failed` / `refunded` |
| `external_id` | **UNIQUE** — `telegram_payment_charge_id` от Telegram |
| `provider` | `telegram_stars` |
| `metadata` | JSONB на будущее |
| `created_at`, `paid_at` | временные отметки |

### `subscription_plans` (+ `device_addon_pricing`)
Добавлена колонка `price_stars INT` — цена в Telegram Stars. Заполнена для всех 4 планов × 3 вариантов устройств = 12 значений.

---

## 🔐 Идемпотентность (самое важное!)

Telegram ретраит webhook до 30 минут если мы ответили 5xx. Может также повторно прислать webhook уже после 200 — из-за сетевых сбоев. **Наш код должен быть идемпотентным**:

```go
// handleSuccessfulPayment:
if existing, _ := s.repo.GetByExternalID(ctx, charge_id); existing != nil {
    return "paid_duplicate", nil   // 200 OK, БД не меняем
}
```

Плюс защита на уровне БД: `external_id` колонка имеет `UNIQUE`, что предотвращает двойное начисление даже в race condition.

**e2e проверено:** повторный webhook с `TEST_CHARGE_123` → `paid_duplicate`, подписка не дублируется.

---

## 🔒 Защита webhook

Webhook — единственная **публичная** ручка в нашем API которую мы не защитили JWT'ом (иначе как бы Telegram авторизовывался?). Защита — через shared secret:

1. При `setWebhook` передаём `secret_token=XXX`
2. Telegram кладёт это значение в header `X-Telegram-Bot-Api-Secret-Token` на каждом запросе
3. Gateway `paymentHandler.TelegramWebhook` сверяет header с env-переменной `TELEGRAM_WEBHOOK_SECRET`
4. Если не совпадает — 403 Forbidden

**e2e проверено:** webhook без секрета → 403.

---

## 💰 Цены (dev)

| План | 2 устр. | 5 устр. | 10 устр. |
|---|---:|---:|---:|
| 1 мес | 100⭐ | 200⭐ | 350⭐ |
| 3 мес | 250⭐ | 500⭐ | 900⭐ |
| 6 мес | 450⭐ | 900⭐ | 1600⭐ |
| 12 мес | 800⭐ | 1600⭐ | 2800⭐ |

Цены — в `subscription-service` миграция 002. При желании меняются простым UPDATE + restart.

---

## 🎬 Сценарии

### ✅ Удачная покупка
```
POST /api/v1/payments {plan_id:1, max_devices:2}
  → Payment service: INSERT pending payment + tg.createInvoiceLink
  → 200 {payment_id: 1, invoice_link: "t.me/$...", amount_stars: 100}

Mini App открывает link через Telegram.WebApp.openInvoice(link)
Юзер платит → Telegram шлёт 2 webhook'а:

1. pre_checkout_query
  → payment-service проверяет что payment_id ещё pending
  → tg.answerPreCheckoutQuery(ok=true)
  
2. successful_payment (charge_id=XYZ)
  → idempotency check (not in DB) 
  → MarkPaid + CreateSubscription + CreateVPNUser
  → 200 {action: "paid"}

Mini App получает VLESS-ссылку через GET /api/v1/vpn/servers/:id/link
```

### ❌ Дубликат webhook
Telegram сетевой сбой — повторно шлёт тот же `successful_payment`:
```
→ check external_id=charge_id → existing payment found → skip
→ 200 {action: "paid_duplicate"}  # БД не меняется
```

### ❌ Невалидный pre_checkout
Юзер нажал "оплатить" спустя час, мы автофейлили pending через cron:
```
pre_checkout_query → payment status ≠ pending
→ tg.answerPreCheckoutQuery(ok=false, error_message="заказ уже обработан")
```

### 💸 Refund (Telegram возвращает Stars в течение 21 дня)
```
refunded_payment webhook
  → UPDATE payments SET status='refunded'
  → sub.CancelSubscription(subscription_id)
  → vpn.DisableVPNUser(user_id) — RemoveUser из Xray + DELETE vpn_users
  → 200 {action: "refunded"}
```

### ⏰ Истечение подписки (cron subscription-service)
Раз в 10 минут:
```
UPDATE subscriptions SET status='expired' WHERE expires_at < NOW() AND status='active' RETURNING user_id
for user_id in expired:
  vpn.DisableVPNUser(user_id)
```

---

## 🗺️ Где это в коде

| Файл | Что |
|---|---|
| [`shared/proto/payment/v1/payment.proto`](../../shared/proto/payment/v1/payment.proto) | gRPC API сервиса (CreateInvoice, GetPayment, ListUserPayments, HandleTelegramUpdate) |
| [`services/payment-service/`](../../services/payment-service/) | Весь сервис: model + repository + service + api + app + cmd/main + Dockerfile |
| [`services/payment-service/migrations/001_create_payments.up.sql`](../../services/payment-service/migrations/001_create_payments.up.sql) | Таблица `payments` с UNIQUE(external_id) |
| [`services/subscription-service/migrations/002_add_stars_price.up.sql`](../../services/subscription-service/migrations/002_add_stars_price.up.sql) | `+price_stars` в subscription_plans и device_addon_pricing + seed цен |
| [`services/subscription-service/internal/service/expire_cron.go`](../../services/subscription-service/internal/service/expire_cron.go) | Cron истечения подписок (каждые 10 мин) → DisableVPNUser |
| [`services/vpn-service/internal/service/vpn.go`](../../services/vpn-service/internal/service/vpn.go) | `DisableVPNUser` — RemoveUser из всех Xray + DELETE vpn_users |
| [`platform/pkg/telegram/client.go`](../../platform/pkg/telegram/client.go) | Минимальный Bot API клиент (createInvoiceLink, answerPreCheckoutQuery, refundStarPayment) |
| [`services/gateway/internal/handler/payment.go`](../../services/gateway/internal/handler/payment.go) | HTTP endpoints: `POST /api/v1/payments`, `GET /api/v1/payments`, `POST /api/v1/telegram/webhook` |
| [`services/gateway/internal/client/payment.go`](../../services/gateway/internal/client/payment.go) | gRPC-клиент payment-service |

---

## 🧪 Как воспроизвести e2e

```bash
# 1. Поднять стек
task compose:up

# 2. Подготовить user + pending payment в БД (как будто CreateInvoice прошёл)
docker exec vpn-postgres psql -U vpn -d vpn -c \
  "INSERT INTO users (telegram_id, first_name) VALUES (12345, 'E2E')
   ON CONFLICT (telegram_id) DO UPDATE SET first_name='E2E' RETURNING id;"

docker exec vpn-postgres psql -U vpn -d vpn -c \
  "INSERT INTO payments (user_id, plan_id, max_devices, amount_stars, status, provider)
   VALUES (1, 1, 2, 100, 'pending', 'telegram_stars') RETURNING id;"

# 3. Симулируем webhook "successful_payment"
SECRET=$(grep '^PAYMENT_TELEGRAM_WEBHOOK_SECRET=' deploy/env/.env | cut -d= -f2-)
curl -X POST http://localhost:8081/api/v1/telegram/webhook \
  -H "Content-Type: application/json" \
  -H "X-Telegram-Bot-Api-Secret-Token: $SECRET" \
  -d '{
    "update_id": 1,
    "message": {
      "from": {"id": 12345},
      "successful_payment": {
        "currency": "XTR",
        "total_amount": 100,
        "invoice_payload": "1",
        "telegram_payment_charge_id": "TEST_CHARGE_123"
      }
    }
  }'
# Ожидаем: {"action":"paid","ok":true}

# 4. Проверить: payment paid, subscription active, vpn_user создан
docker exec vpn-postgres psql -U vpn -d vpn -c \
  "SELECT status, external_id FROM payments;"
# Ожидаем: paid | TEST_CHARGE_123

docker exec vpn-postgres psql -U vpn -d vpn -c \
  "SELECT status FROM subscriptions;"
# Ожидаем: active

docker exec vpn-postgres psql -U vpn -d vpn -c \
  "SELECT user_id, email FROM vpn_users;"
# Ожидаем: 1 | user1@vpn.local

# 5. Idempotency: повторный webhook — БД не меняется
# (пошли тот же JSON)
# Ожидаем: {"action":"paid_duplicate","ok":true}

# 6. Refund flow
curl -X POST http://localhost:8081/api/v1/telegram/webhook \
  -H "Content-Type: application/json" \
  -H "X-Telegram-Bot-Api-Secret-Token: $SECRET" \
  -d '{... "refunded_payment": {..."telegram_payment_charge_id":"TEST_CHARGE_123"}}'
# Ожидаем: {"action":"refunded","ok":true}
# payments.status=refunded, subscriptions.status=cancelled, vpn_users пусто
```

---

## 🔧 Setup на проде

1. **@BotFather → Payments** — переключить на **Live** режим (на dev всегда Test)
2. **Webhook URL** зарегистрировать:
   ```bash
   curl "https://api.telegram.org/bot$TOKEN/setWebhook" \
     -d "url=https://api.maydavpn.com/api/v1/telegram/webhook&secret_token=<ваш secret>"
   ```
3. **Сверить:**
   ```bash
   curl "https://api.telegram.org/bot$TOKEN/getWebhookInfo"
   # pending_update_count: 0, last_error: null
   ```
4. `TELEGRAM_WEBHOOK_SECRET` в env **обязательно** должен совпадать с `secret_token` из setWebhook.

---

## ⚠️ Известные ограничения / TODO

- **Auto-fail pending cron** — если юзер закрыл Mini App не оплатив, payment остаётся pending. Надо cron в payment-service раз в 1 час: `UPDATE status='failed' WHERE status='pending' AND created_at < NOW() - 1 hour`. **TODO**, не критично.
- **No retry для CreateSubscription/CreateVPNUser** — если Telegram отправил webhook, но `sub.CreateSubscription` упал (БД легла на секунду) — мы вернём 500, Telegram ретраит через минуту. Но сейчас нет механизма "видеть" что payment в БД уже `paid`, а подписка не создалась. Хрупкая точка. **TODO:** двухфазный commit или outbox pattern.
- **Refund race:** если Telegram refund прилетит между `MarkPaid` и `CreateSubscription` — получится "paid без подписки". Очень редко, но возможно. **TODO:** transactional outbox.

---

## 📚 См. также

- [xray-integration.md](./xray-integration.md) — как VPN Service общается с Xray
- [device-limit.md](./device-limit.md) — лимит устройств
- [auth-middleware.md](./auth-middleware.md) — JWT-защита ручек
- [Telegram Bot API — Payments](https://core.telegram.org/bots/api#payments)
- [Telegram Stars](https://core.telegram.org/bots/payments-stars)
