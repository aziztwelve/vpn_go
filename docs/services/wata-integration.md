# WATA Integration — приём платежей через H2H API

Как подключить [WATA Payment API](https://wata.pro/api) как третий провайдер оплаты рядом с Telegram Stars и YooMoney. Поддерживает карты (МИР/VISA/MC), СБП, T-Pay, SberPay.

**Статус реализации:** 📋 **план** (только документация, код ещё не написан)

---

## 🧠 Простыми словами

Юзер выбирает тариф и в качестве способа оплаты жмёт "💳 WATA". Backend создаёт **платёжную ссылку** через WATA API (`POST /links`), получает URL → открывает его юзеру. Юзер платит на форме WATA (карта / СБП / T-Pay), WATA шлёт нам **webhook** о статусе, мы проверяем RSA-подпись и активируем подписку.

Это единственный сценарий — **H2H (Host-to-Host) через платёжную ссылку**, без собственной формы ввода карточных данных.

---

## 📋 Регистрация и доступы

1. Получить приглашение от личного менеджера WATA по email
2. Задать пароль, войти в ЛК мерчанта: <https://merchant.wata.pro/login>
3. В разделе «Терминалы» → нужный терминал → создать **Access Token** (JWT)
   - Срок жизни: **1–12 месяцев** (выбираем 12)
   - Токен **невосстановим** — показывается один раз, хранить в secret store
   - В ЛК можно иметь от 1 до 5 активных токенов одновременно
4. На тех же настройках терминала прописать:
   - **Webhook URL** (обязателен) — `https://api.osmonai.com/api/v1/payments/wata/webhook`
   - **Success Page** — `https://cdn.osmonai.com/payment/success`
   - **Fail Page** — `https://cdn.osmonai.com/payment/fail`
5. Для теста запросить у менеджера **sandbox-учётку** — боевой логин/пароль в песочнице не работают

### Окружения

| Env | API base URL | ЛК |
|---|---|---|
| Prod | `https://api.wata.pro/api/h2h` | <https://merchant.wata.pro> |
| Sandbox | `https://api-sandbox.wata.pro/api/h2h` | <https://merchant-sandbox.wata.pro> |

### Тестовые карты (sandbox)

| Тип | Номер | Результат |
|---|---|---|
| МИР без 3DS | `2200 0000 2222 2222` | Success |
| МИР без 3DS | `2203 0000 0000 0043` | Declined |
| VISA с 3DS | `4242 4242 4242 4242` | Success |
| VISA с 3DS | `4012 8888 8888 1881` | Declined |

---

## 🔑 Аутентификация

Все вызовы API — Bearer JWT в заголовке:

```
Authorization: Bearer <access-token>
Content-Type: application/json
```

После истечения срока — `401`. **Напоминание за 2 недели до истечения** надо завести отдельно (cron / календарь админа), т.к. WATA ничего не подскажет, а регенерация требует ручного входа в ЛК + рестарта контейнера с новым env.

---

## 🧩 Эндпоинты, которыми пользуемся

| Метод | Путь | Зачем |
|---|---|---|
| `POST` | `/links` | Создать платёжную ссылку → получить `url` для редиректа |
| `GET` | `/links/{id}` | Инфо по ссылке (лимит: 1 req / 30 sec на id) |
| `GET` | `/transactions/{id}` | Инфо по транзакции (лимит: 1 req / 30 sec) |
| `GET` | `/transactions?orderId=…` | Поиск транзакций по нашему orderId |
| `POST` | `/transactions/refunds` | Refund по транзакции в статусе `Paid` |
| `GET` | `/public-key` | Публичный RSA-ключ для проверки webhook-подписи |

> **Rate limit на GET:** 1 запрос / 30 сек на один объект (links/{id}, transactions/{id}, или `orderId`).
> Для polling не используем — **webhooks с SLA доставки 32 часа — primary source**. GET только для ручной диагностики и fallback (если webhook не пришёл).

---

## 📤 Создание платёжной ссылки

```http
POST /api/h2h/links
Authorization: Bearer <token>
Content-Type: application/json

{
  "type": "OneTime",                 // OneTime (default) или ManyTime
  "amount": 499.00,                  // min 10 RUB, max 999 999.99
  "currency": "RUB",                 // RUB | USD | EUR
  "orderId": "payment-123",          // наш payment.id — на него опираемся в webhook
  "description": "VPN 3 месяца × 1 устройство",
  "successRedirectUrl": "https://cdn.osmonai.com/payment/success",
  "failRedirectUrl":    "https://cdn.osmonai.com/payment/fail",
  "expirationDateTime": "2026-04-27T12:00:00Z"  // 10min…30d, default 3 дня
}
```

**Response 200:**

```json
{
  "id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
  "url": "https://app.wata.pro/pay/...",       // редиректим сюда
  "status": "Opened",
  "amount": 499.00,
  "currency": "RUB",
  "terminalName": "OsmonAI-Main",
  "terminalPublicId": "3a16a4dd-8c83-...",
  "creationTime": "2025-04-24T17:00:00Z",
  "expirationDateTime": "2025-04-27T17:00:00Z",
  "orderId": "payment-123"
}
```

У нас в payment.external_id сохраняем `id` ссылки (UUID), а в invoice_link — `url`. Фронт редиректит юзера на `url` через `webApp.openLink()`.

---

## 📥 Webhook — единственный надёжный канал статусов

WATA шлёт POST на `Webhook URL` терминала. **Типы уведомлений:**

| Тип | Когда | SLA / таймаут |
|---|---|---|
| **Предоплата** | До запроса в банк | **10 сек** — если не ответили 200, транзакция отклоняется |
| **Постоплата** | После подтверждения банка (Paid/Declined) | 1 мин таймаут, **ретраи 32 часа** с возрастающим интервалом |
| **Возврат** | После завершения рефанда | 1 мин, ретраи 32 часа |

### Пример payload

```http
POST /api/v1/payments/wata/webhook
Content-Type: application/json
X-Signature: base64(rsa_sha512(raw_body))

{
  "transactionType": "CardCrypto",      // CardCrypto | SBP | TPay | SberPay
  "kind": "Payment",                    // Payment | Refund
  "id": "3a1cf611-abc6-...",            // UUID платёжной ссылки
  "transactionId": "3a16a4f0-27b0-...", // UUID транзакции
  "transactionStatus": "Paid",          // Created | Pending | Paid | Declined
  "terminalPublicId": "3b16a2f1-...",
  "amount": 499.00,
  "currency": "RUB",
  "orderId": "payment-123",             // ключ dedup — наш payment.id
  "orderDescription": "VPN 3 месяца × 1 устройство",
  "paymentTime": "2025-04-24T17:15:00Z",
  "commission": 10.00,
  "email": null,
  "paymentLinkId": "3a1cf611-abc6-...",
  "errorCode": null,
  "errorDescription": null
}
```

### Обязательные правила обработки

1. **Всегда отвечать 200 OK** — иначе ретраи 32 часа / отклонение предоплаты
2. **Dedup по orderId** — одна платёжная ссылка может породить несколько транзакций (failed attempts). Проверяем `payment.external_id` + `transactionStatus`, не активируем подписку повторно
3. **Опираемся на `orderId`**, НЕ на `paymentLinkId` — ссылка удаляется из WATA после `expirationDateTime`, ID можно не найти
4. **Для предоплатного webhook** (если включён в ЛК) — ответить 200 OK только если заказ актуален; 4xx/5xx → транзакция будет отклонена

---

## 🔐 Проверка подписи webhook

Каждый webhook содержит `X-Signature: <base64(rsa_sig)>`. Алгоритм:

1. При старте (и раз в сутки по cron) фетчить публичный ключ:
   ```
   GET https://api.wata.pro/api/h2h/public-key
   → { "value": "-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----" }
   ```
   Формат — **PKCS1**. Кешировать в памяти `paymentService`.
2. При получении webhook:
   - Читать raw body (до парсинга!)
   - base64-decode `X-Signature`
   - `rsa.VerifyPKCS1v15(pub, crypto.SHA512, hash(rawBody), sig)`
3. Если подпись не сошлась → 401, **не** обрабатывать.

### Go-реализация (skeleton)

```go
func (p *WataProvider) VerifyWebhook(rawBody []byte, sigB64 string) error {
    sig, err := base64.StdEncoding.DecodeString(sigB64)
    if err != nil { return fmt.Errorf("bad signature b64: %w", err) }

    hash := sha512.Sum512(rawBody)
    pub := p.publicKey() // cached *rsa.PublicKey, refetched daily

    if err := rsa.VerifyPKCS1v15(pub, crypto.SHA512, hash[:], sig); err != nil {
        return fmt.Errorf("rsa verify: %w", err)
    }
    return nil
}
```

**Важно:** публичный ключ в PKCS1 — парсим через `x509.ParsePKCS1PublicKey` после `pem.Decode`.

---

## ♻️ Refund

```http
POST /api/h2h/transactions/refunds
Authorization: Bearer <token>

{
  "originalTransactionId": "3fa85f64-...",  // UUID транзакции (не ссылки!)
  "amount": 499.00                          // ≤ оригинальной, >0, до 2 знаков
}
```

Возвращает `transactionId` + `transactionStatus: Pending`. Итоговый статус прилетит webhook'ом (`kind: Refund`). Требует у терминала тип **"Эквайринг"** (не "Цифровые товары") — уточнить у менеджера при регистрации.

---

## ❗ Коды ошибок API (частые)

| Код | Смысл |
|---|---|
| `Payment:PL_1001` | Сумма вне допустимого диапазона |
| `Payment:TRA_1005` | Некорректная сумма |
| `Payment:TRA_1013` | Способ оплаты не включён у терминала |
| `Payment:TRA_2002` | Недостаточно средств (у клиента) |
| `Payment:TRA_2004` | Подозрение на мошенничество |
| `Payment:TRA_2018` | Карта просрочена |
| `Payment:TRA_2024` | Превышен лимит операций по карте |

Полный список — в [wata.pro/api](https://wata.pro/api) раздел «Коды ошибок». При создании invoice маппим их в наш `ApiError.code` для локализованных UX-сообщений.

---

## 🔄 Отличия от YooMoney (что нового в коде)

| Аспект | YooMoney (текущий) | WATA (новый) |
|---|---|---|
| Подпись webhook | HMAC-SHA256 (shared secret) | **RSA-SHA512** (public key) |
| Идемпотентность | `Idempotency-Key` header | Нет — dedup по `orderId` |
| Refund | 2-step (createRefund → poll) | 1 запрос `POST /transactions/refunds` |
| Методы оплаты | Карты | Карты + **СБП** + **T-Pay** + **SberPay** |
| Rate limit на GET | Нет | **1 req / 30 sec** на объект |
| Обновление статуса | Polling допустим | **Только webhook** (polling не годится) |
| Валюты | RUB | RUB, EUR, USD |

---

## 📦 План интеграции в `payment-service`

### 1. Конфиг

Добавить в `deploy/env/payment.env.template`:

```bash
PAYMENT_WATA_ENABLED=true
PAYMENT_WATA_BASE_URL=https://api-sandbox.wata.pro/api/h2h   # prod: api.wata.pro
PAYMENT_WATA_ACCESS_TOKEN=<jwt-из-ЛК>
PAYMENT_WATA_SUCCESS_URL=https://cdn.osmonai.com/payment/success
PAYMENT_WATA_FAIL_URL=https://cdn.osmonai.com/payment/fail
PAYMENT_WATA_LINK_TTL=3d                                     # 10m…30d
```

### 2. Новый провайдер `services/payment-service/internal/provider/wata/wata.go`

Реализует существующий интерфейс:

```go
type Provider interface {
    Name() string                                              // "wata"
    CreateInvoice(ctx, *CreateInvoiceRequest) (*Invoice, error)
    HandleWebhook(ctx, rawBody, headers) (*WebhookResult, error)
    Refund(ctx, externalID, amountRUB) error
}
```

Внутри:
- HTTP-клиент с Bearer token и `Content-Type: application/json`
- Публичный ключ WATA кешится в памяти (refresh раз в сутки)
- `CreateInvoice` → `POST /links` с `orderId = payment.ID`
- `HandleWebhook` → RSA-verify → по `orderId` находит наш payment → `Paid` активирует подписку

### 3. Регистрация в `app.go`

```go
// services/payment-service/internal/app/app.go
if cfg.WataEnabled {
    wataProv := wata.New(wata.Config{
        BaseURL:     cfg.WataBaseURL,
        AccessToken: cfg.WataAccessToken,
        SuccessURL:  cfg.WataSuccessURL,
        FailURL:     cfg.WataFailURL,
        LinkTTL:     cfg.WataLinkTTL,
        Logger:      logger,
    })
    providers["wata"] = wataProv
}
```

### 4. Gateway-роут для webhook

В <ref_file file="/root/.openclaw/workspace/vpn/vpn_go/services/gateway/internal/handler/payment.go" /> добавить рядом с yoomoney:

```go
r.Post("/api/v1/payments/wata/webhook", h.WataWebhook)
```

Handler:
1. Читает raw body
2. Форвардит в payment-service gRPC `HandleProviderWebhook(provider="wata", body, headers)`
3. Возвращает `200 OK` **всегда**, даже при ошибке верификации (чтобы WATA не ретраила — логируем и всё)

### 5. Миграция БД (не нужна)

Таблица `payments` уже полиморфная: `provider TEXT`, `external_id TEXT`, `amount_rub NUMERIC`. WATA укладывается без изменений схемы.

### 6. Фронт (vpn_next)

В <ref_file file="/root/.openclaw/workspace/vpn/vpn_next/app/plans/page.tsx" /> добавить третий тумблер провайдера:

```tsx
<button onClick={() => setSelectedProvider('wata')}>
  <div>💳</div>
  <div>Карта / СБП / T-Pay</div>
  <div>Через WATA</div>
</button>
```

При выборе WATA — `createInvoice(..., 'wata')`, получаем `invoice_link`, открываем через `webApp.openLink(url)` в браузере (как YooMoney).

### 7. Тесты

- Юнит: подпись webhook (positive + negative), парсинг PKCS1 public key, обработка 429/500 от WATA API
- Интеграционный: sandbox — создать link → оплатить тестовой картой → проверить, что webhook пришёл и подписка активировалась

---

## 🔗 Ссылки

- Публичная дока: <https://wata.pro/api>
- EN-версия: <https://wata.pro/api_en>
- ЛК prod: <https://merchant.wata.pro>
- ЛК sandbox: <https://merchant-sandbox.wata.pro>
- Публичный ключ prod: <https://api.wata.pro/api/h2h/public-key>
