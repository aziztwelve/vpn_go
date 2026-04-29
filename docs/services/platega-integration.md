# Platega Integration — приём платежей через Platega API

Как подключить [Platega API](https://docs.platega.io) как ещё один провайдер оплаты рядом с Telegram Stars / YooMoney / WATA. Поддерживает СБП (QR), ЕРИП, карточный эквайринг, международные карты, криптовалюту.

**Статус реализации:** 🚧 **в разработке** — код есть (`services/payment-service/internal/provider/platega/platega.go` + unit-тесты), интеграция на gateway включена, фронт переключён на Platega как **единственный** активный провайдер. Остаётся прогнать e2e в sandbox-аккаунте Platega (нужны реальные `PAYMENT_PLATEGA_MERCHANT_ID` / `PAYMENT_PLATEGA_API_SECRET` от менеджера).

---

## 🧠 Простыми словами

Юзер выбирает тариф и в качестве способа оплаты жмёт "💳 Platega". Backend создаёт **платёжную ссылку** через Platega API (`POST /v2/transaction/process` или `POST /transaction/process`), получает `url` → открывает его юзеру. Юзер платит на форме Platega (СБП / карта / ЕРИП / крипта), Platega шлёт нам **callback** о статусе, мы сверяем `X-MerchantId` + `X-Secret` в заголовках и активируем подписку.

Это сценарий **redirect-to-payform** — собственная форма ввода реквизитов нам не нужна.

---

## 📋 Регистрация и доступы

1. Получить приглашение от менеджера Platega → доступ к ЛК
2. В ЛК на странице «Настройки» забрать:
   - **MerchantId** — UUID магазина
   - **API Secret** — API-ключ (показывается в ЛК, можно регенерировать)
3. На странице «Настройки → Callback URLs» прописать:
   - **Callback URL** — `https://api.osmonai.com/api/v1/payments/platega/callback`
4. (опционально) success/fail страницы передаются в каждом запросе на создание ссылки — общие можно не задавать в ЛК

### Окружение

| Env | API base URL | ЛК |
|---|---|---|
| Prod | `https://app.platega.io` | <https://app.platega.io> |

> Sandbox-окружения у Platega как такового нет — для тестов менеджер выдаёт отдельный мерчант-аккаунт с пониженными лимитами / тестовыми способами оплаты. Уточнять при подключении.

### Требования к Callback URL

Жёсткие, валидируются на стороне Platega ещё при сохранении в ЛК:

- ✅ Только **HTTPS** (HTTP запрещён)
- ✅ Только публичный IP или доменное имя
- ✅ Корректный SSL от доверенного УЦ
- ❌ Self-signed сертификаты
- ❌ Приватные IP-диапазоны (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `127.0.0.0/8`)
- ❌ `localhost` / loopback

Для локальной разработки используем `cloudflared` или `ngrok` с белым SSL.

---

## 🔑 Аутентификация

Все вызовы API — два заголовка:

```
X-MerchantId: <UUID>
X-Secret:     <API-ключ>
Content-Type: application/json
```

При неверных кредах — `401 Ошибка аутентификации`. Срок жизни ключа не ограничен, но при компрометации регенерируется в ЛК (старый сразу инвалидируется → нужен рестарт сервиса с новым env).

---

## 🧩 Эндпоинты, которыми пользуемся

| Метод | Путь | Зачем |
|---|---|---|
| `POST` | `/v2/transaction/process` | Создать платёжную ссылку **без** заданного метода (юзер выбирает на форме) |
| `POST` | `/transaction/process` | Создать ссылку **с** заданным методом (например, сразу СБП QR) |
| `GET` | `/transaction/{id}` | Статус и детали транзакции (fallback к callback) |
| `GET` | `/rates/payment_method_rate` | Курс валюты для конкретного метода (например, RUB → USDT) |
| `GET` | `/balance/all` | Балансы магазина (RUB / USDT, frozen) — для админки/мониторинга |

> **Polling vs callback:** primary канал статусов — **callback**. `GET /transaction/{id}` дёргаем только для ручной диагностики или если callback не пришёл за разумное время (>10 мин).

### Способы оплаты (PaymentMethodInt)

| Код | Метод |
|---|---|
| `2` | СБП (QR-код) |
| `3` | ЕРИП |
| `11` | Карточный эквайринг (РФ) |
| `12` | Международная оплата (карты) |
| `13` | Криптовалюта |

---

## 📤 Создание платёжной ссылки

### Вариант A — без заданного метода (предпочтительный для VPN)

Юзеру отдаём универсальный URL Platega → он сам выбирает способ оплаты на форме.

```http
POST /v2/transaction/process
X-MerchantId: <merchant-id>
X-Secret: <api-key>
Content-Type: application/json

{
  "paymentDetails": {
    "amount": 499.00,
    "currency": "RUB"
  },
  "description": "VPN 3 месяца × 1 устройство",
  "return":    "https://cdn.osmonai.com/payment/success",
  "failedUrl": "https://cdn.osmonai.com/payment/fail",
  "payload":   "payment-123"
}
```

**Response 200:**

```json
{
  "transactionId": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
  "status": "PENDING",
  "url": "https://pay.platega.io/?id=f8000067-...&mh=0a0000a4-...",
  "expiresIn": "00:15:00",
  "rate": 91.2
}
```

### Вариант B — с заданным методом

Если хотим сразу открыть конкретную форму (например, СБП-куар):

```http
POST /transaction/process
{
  "paymentMethod": 2,
  "paymentDetails": { "amount": 499.00, "currency": "RUB" },
  "description": "VPN 3 месяца × 1 устройство",
  "return":    "https://cdn.osmonai.com/payment/success",
  "failedUrl": "https://cdn.osmonai.com/payment/fail",
  "payload":   "payment-123"
}
```

**Response 200:**

```json
{
  "paymentMethod": "SBPQR",
  "transactionId": "3fa85f64-5717-4562-b3fc-2c463f66afa6",
  "redirect": "https://pay.platega.io?qrsbp",
  "return": "https://cdn.osmonai.com/payment/success",
  "paymentDetails": { "amount": 499.00, "currency": "RUB" },
  "status": "PENDING",
  "expiresIn": "00:15:00",
  "merchantId": "1a021d91-9b26-4762-b303-5d4aac74e921",
  "usdtRate": 93.45
}
```

> **Ключевые правила:**
> - **`id` транзакции НЕ передаём** — генерирует Platega.
> - **`payload`** — наше сквозное поле; кладём туда `payment.id` (или его UUID), чтобы потом сматчить callback.
> - **`description`** обязательно. Для продажи Stars нужен формат `TgId:<id> UserId:<id>` — **нам не релевантно** (мы продаём VPN, не Stars).
> - **`expiresIn`** — TTL ссылки `00:15:00` (15 мин). Если юзер замешкался — создаём новую.

В payment.external_id сохраняем `transactionId`, в invoice_link — `url` (для v2) или `redirect` (для v1). Фронт открывает через `webApp.openLink(url)`.

---

## 📥 Callback — primary канал статусов

Platega шлёт `POST` на Callback URL из ЛК.

### Заголовки

```
X-MerchantId: <тот же UUID, что и наш>
X-Secret:     <тот же API-ключ, что и у нас>
Content-Type: application/json
```

### Body

```json
{
  "id": "00000000-0000-0000-0000-000000000000",
  "amount": 499.00,
  "currency": "RUB",
  "status": "CONFIRMED",
  "paymentMethod": 2,
  "payload": "payment-123"
}
```

### Статусы

| Status | Что это |
|---|---|
| `CONFIRMED` | Оплата прошла — активируем подписку |
| `CANCELED` | Юзер не оплатил / отменил / истёк TTL |
| `CHARGEBACKED` | **Возврат / chargeback** после успешной оплаты — нужно отозвать подписку |

> Статус `PENDING` существует в API (на этапе создания), но в callback не приходит — callback шлётся только на финальные переходы.

### Обязательные правила обработки

1. **Сверка кредов из заголовков** — `X-MerchantId` и `X-Secret` должны побайтно совпасть с нашими env (`subtle.ConstantTimeCompare`). Если нет — `401`, не обрабатывать. Это и есть единственный механизм аутентификации callback'а — никакой подписи payload **нет**.
2. **Dedup по `payload`** — там лежит наш `payment.id`. Проверяем, что подписку ещё не активировали.
3. **Отвечать 200 OK** в течение **60 секунд**, иначе Platega ретраит до 3 раз с интервалом 5 минут.
4. **`CHARGEBACKED`** обрабатываем отдельно: **отзываем подписку** + помечаем `payment.status = chargebacked` + уведомляем админа в Telegram-канал.

### Ретраи

| Попытка | Когда |
|---|---|
| 1-я | Сразу |
| Таймаут | 60 секунд без 200 → отмена |
| 2-я | Через 5 минут |
| 3-я | Ещё через 5 минут |
| 4-я | Ещё через 5 минут |

После 3 ретраев Platega сдаётся → ловим расхождение через ручной `GET /transaction/{id}` или sentinel cron «висящие PENDING > 30 мин».

---

## 🔐 Безопасность callback

В отличие от WATA (RSA-SHA512) у Platega **подписи payload нет** — аутентификация только через сравнение `X-MerchantId` + `X-Secret`. Это слабее, но достаточно при условии:

- Callback URL ходит **только по HTTPS** (TLS защищает заголовки от перехвата)
- `X-Secret` хранится в Vault / env, не логируется
- Сверяем оба заголовка через **constant-time compare**, чтобы не было timing-атак на подбор секрета

```go
func (p *PlategaProvider) verifyCallback(headers http.Header) error {
    mid := headers.Get("X-Merchantid")
    sec := headers.Get("X-Secret")

    midOK := subtle.ConstantTimeCompare([]byte(mid), []byte(p.merchantID)) == 1
    secOK := subtle.ConstantTimeCompare([]byte(sec), []byte(p.apiSecret))  == 1

    if !midOK || !secOK {
        return errors.New("platega callback: invalid creds")
    }
    return nil
}
```

> Дополнительная защита (опционально): IP allowlist на Cloudflare/nginx с диапазонами Platega — попросить у менеджера.

---

## ♻️ Refund

В публичной API-доке отдельного refund-эндпоинта **нет**. Возвраты инициируются:

- через ЛК Platega (ручной процесс саппорта)
- либо через chargeback от банка-эмитента

В обоих случаях прилетает callback со статусом **`CHARGEBACKED`** → его и обрабатываем.

Если потребуется автоматический refund — уточнить у менеджера, есть ли закрытый API-метод (в публичной доке его нет на момент ).

---

## 🧮 Курсы и балансы (вспомогательные эндпоинты)

### Курс валюты для метода

```http
GET /rates/payment_method_rate?merchantId=<uuid>&paymentMethod=13&currencyFrom=RUB&currencyTo=USDT
X-MerchantId: <uuid>
X-Secret: <key>
```

Ответ:

```json
{
  "paymentMethod": 13,
  "currencyFrom": "RUB",
  "currencyTo": "USDT",
  "rate": 0.0105,
  "updatedAt": "2025-08-11T10:15:00Z"
}
```

Используем для отображения цены в крипте (если включаем method=13) и для preview комиссии.

### Балансы магазина

```http
GET /balance/all
X-MerchantId: <uuid>
X-Secret: <key>
```

Ответ:

```json
[
  { "amount": 15000.5, "currency": "RUB" },
  { "amount": 200,     "currency": "USDT", "frozenBalance": 500 }
]
```

Дёргаем cron'ом раз в сутки → пишем в Prometheus для алертинга «баланс < N».

---

## 🔄 Отличия от других провайдеров

| Аспект | YooMoney | WATA | **Platega** |
|---|---|---|---|
| Auth API | OAuth2 access_token | Bearer JWT | **`X-MerchantId` + `X-Secret`** |
| Подпись webhook | HMAC-SHA256 | RSA-SHA512 | **Нет** (только сравнение `X-Secret`) |
| Идемпотентность | `Idempotency-Key` | dedup по `orderId` | **dedup по `payload`** |
| Refund | API 2-step | API 1 запрос | **Только из ЛК / chargeback** |
| Методы | Карты | Карты + СБП + T-Pay + SberPay | **СБП QR + ЕРИП + Карты РФ + Карты Intl + Crypto** |
| TTL ссылки | долгий | 10m…30d | **15 минут (фикс)** |
| Sandbox | есть | есть | отдельный мерч-аккаунт |
| Валюты | RUB | RUB/EUR/USD | RUB / USDT (через крипто-метод) |

---

## 📦 План интеграции в `payment-service`

### 1. Конфиг

Добавить в `deploy/env/payment.env.template`:

```bash
PAYMENT_PLATEGA_ENABLED=true
PAYMENT_PLATEGA_BASE_URL=https://app.platega.io
PAYMENT_PLATEGA_MERCHANT_ID=<uuid-из-ЛК>
PAYMENT_PLATEGA_API_SECRET=<ключ-из-ЛК>
PAYMENT_PLATEGA_SUCCESS_URL=https://cdn.osmonai.com/payment/success
PAYMENT_PLATEGA_FAIL_URL=https://cdn.osmonai.com/payment/fail
PAYMENT_PLATEGA_DEFAULT_METHOD=                # пусто = юзер выбирает на форме (v2)
                                               # или 2/3/11/12/13 для конкретного метода
```

### 2. Новый провайдер `services/payment-service/internal/provider/platega/platega.go`

Реализует существующий интерфейс (как `wata` / `yoomoney`):

```go
type Provider interface {
    Name() string                                              // "platega"
    CreateInvoice(ctx, *CreateInvoiceRequest) (*Invoice, error)
    HandleWebhook(ctx, rawBody, headers) (*WebhookResult, error)
    Refund(ctx, externalID, amountRUB) error                   // ErrUnsupported
}
```

Внутри:
- HTTP-клиент с заголовками `X-MerchantId` / `X-Secret`
- `CreateInvoice`:
  - если `defaultMethod == ""` → `POST /v2/transaction/process`
  - иначе → `POST /transaction/process` с `paymentMethod = defaultMethod`
  - `payload = payment.ID`
- `HandleWebhook`:
  - сверить `X-MerchantId` + `X-Secret` через `subtle.ConstantTimeCompare`
  - распарсить body → найти `payment` по `payload`
  - `CONFIRMED` → активировать подписку
  - `CANCELED` → пометить payment failed
  - `CHARGEBACKED` → отозвать подписку + алерт
- `Refund` → возвращает `ErrUnsupported` (документировать, что делается из ЛК)

### 3. Регистрация в `app.go`

```go
// services/payment-service/internal/app/app.go
if cfg.PlategaEnabled {
    plategaProv := platega.New(platega.Config{
        BaseURL:       cfg.PlategaBaseURL,
        MerchantID:    cfg.PlategaMerchantID,
        APISecret:     cfg.PlategaAPISecret,
        SuccessURL:    cfg.PlategaSuccessURL,
        FailURL:       cfg.PlategaFailURL,
        DefaultMethod: cfg.PlategaDefaultMethod,  // "" or "2"/"3"/"11"/"12"/"13"
        Logger:        logger,
    })
    providers["platega"] = plategaProv
}
```

### 4. Gateway-роут для callback

В <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/gateway/internal/handler/payment.go" /> рядом с yoomoney/wata:

```go
r.Post("/api/v1/payments/platega/callback", h.PlategaCallback)
```

Handler:
1. Читает raw body
2. Прокидывает в payment-service gRPC `HandleProviderWebhook(provider="platega", body, headers)`
3. **Возвращает `200 OK` всегда** (даже при ошибке валидации) — иначе Platega ретраит 3×5 минут впустую; сами ошибки логируем + Sentry

### 5. Миграция БД (не нужна)

Таблица `payments` уже полиморфная: `provider TEXT`, `external_id TEXT`, `amount_rub NUMERIC`. Platega укладывается без изменений схемы.

Для `CHARGEBACKED` добавить значение в enum `payment.status` (если он строгий) или просто строкой — в зависимости от текущей схемы.

### 6. Фронт (vpn_next)

В <ref_file file="/root/.openclaw/workspace/vpn/vpn_next/app/plans/page.tsx" /> добавить четвёртый тумблер провайдера:

```tsx
<button onClick={() => setSelectedProvider('platega')}>
  <div>💳</div>
  <div>СБП / Карта / ЕРИП / Crypto</div>
  <div>Через Platega</div>
</button>
```

При выборе Platega — `createInvoice(..., 'platega')`, получаем `invoice_link` → `webApp.openLink(url)`.

### 7. Тесты

- **Юнит:**
  - сверка `X-MerchantId` + `X-Secret` (positive + negative + timing-safe)
  - парсинг callback payload (все 3 статуса)
  - выбор v1 vs v2 endpoint в зависимости от `DefaultMethod`
- **Интеграционный:**
  - тестовый мерчант — создать link → оплатить → проверить, что callback пришёл и подписка активировалась
  - смоделировать `CHARGEBACKED` → подписка отозвана, алерт ушёл

### 8. Мониторинг

- **Метрики Prometheus:**
  - `platega_invoice_created_total{method=...}`
  - `platega_callback_received_total{status=...}`
  - `platega_callback_invalid_creds_total` — алерт если >0
- **Cron:** дёргать `GET /balance/all` раз в сутки → метрика `platega_balance_rub` → алерт «баланс < 10000 RUB»
- **Sentinel:** payments в `PENDING` > 30 минут → дёрнуть `GET /transaction/{id}` для синхронизации

---

## ❗ Особые случаи

### Когда callback не пришёл вообще

Platega ретраит 3 раза с интервалом 5 минут. Если нашему gateway/payment-service было плохо больше 20 минут — расхождение нужно лечить вручную:

1. Cron «висящие PENDING > 30 мин» → `GET /transaction/{id}` для каждого
2. Если статус `CONFIRMED` → активировать подписку, отметить `recovered_via_polling`
3. Если `CANCELED` → пометить payment failed

### `CHARGEBACKED` после успешной активации

Самый болезненный кейс — юзер уже пользуется VPN, а деньги вернули.

1. Отозвать подписку (`subscription.expires_at = now()`)
2. Удалить юзера из всех Xray-серверов (graceful — пусть допользует пока сессия не отвалится)
3. Алерт админу в Telegram-канал «🚨 Chargeback: payment {id}, user {tg_id}, amount {amount}»
4. Пометить юзера в антифрод-таблице (если их два за месяц — бан)

### Разные валюты

Platega поддерживает RUB / USDT. Если решим включить криптометод (`paymentMethod=13`):

- передаём `currency: "RUB"` (Platega сама конвертирует по курсу из `GET /rates/payment_method_rate`)
- в callback придёт всё та же сумма в RUB
- юзеру на форме покажется эквивалент в USDT

---

## 🔗 Ссылки

- Публичная дока: <https://docs.platega.io>
- LLMs.txt (полный конспект): <https://docs.platega.io/llms.txt>
- ЛК prod: <https://app.platega.io>
- API base: `https://app.platega.io`
