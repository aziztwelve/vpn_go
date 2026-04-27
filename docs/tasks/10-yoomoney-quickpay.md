# 10. ЮMoney Quickpay — приём оплаты на кошелёк проекта

**Дата:** 2026-04-25
**Статус:** 🟢 Утверждено — все 4 открытых вопроса закрыты, в работе
**Автор:** Devin + aziz
**Родительские документы:**
- [services/yoomoney-integration.md](../services/yoomoney-integration.md) — план интеграции (writeup)
- [services/yoomoney-api-reference.md](../services/yoomoney-api-reference.md) — справочник всех 22 страниц официальной доки
- [services/payment-integration.md](../services/payment-integration.md) — паттерн multi-provider, на нём сидим
- [services/wata-integration.md](../services/wata-integration.md) — соседний платёжный провайдер, образец wiring

---

## 🎯 Цель

Юзер VPN-бота может оплатить подписку **с банковской карты в рублях**
(альтернатива Telegram Stars). Деньги падают на кошелёк ЮMoney проекта,
backend получает HTTP-уведомление, активирует подписку. Без OAuth, без
интеграции «продвинутых» методов API кошелька.

Минимальная фича для запуска RU-картам мимо Stars: открыли форму ЮMoney →
оплатили картой → через ~10 секунд подписка активна, юзеру в бот падает
сообщение «оплата прошла».

---

## 📚 Текущее состояние (важно — не с нуля!)

Код-заглушка частично уже **есть в репе**, но **не работает**:

| Что | Где | Состояние |
|---|---|---|
| `YooMoneyProvider` структура | `services/payment-service/internal/provider/yoomoney/yoomoney.go` | Есть, **с багами** |
| Регистрация провайдера в `app.go` | `services/payment-service/internal/app/app.go` § `initProviders()` | **Закомментирована** (строки 132–144) |
| Роутинг webhook'а | `services/gateway/internal/handler/payment.go` § `HandleWebhook` | Есть `case "yoomoney"`, проброс работает |
| Колонки в БД (`provider`, `amount_rub`, `currency`) | `services/payment-service/migrations/002_add_multi_provider_support.up.sql` | Есть, миграция применена |
| Конфиг (`YooMoneyConfig` struct, env-vars) | `services/payment-service/internal/config/config.go` | **Нет** |
| `.env.template` | `deploy/env/{.env,payment.env}.template` | **Нет** |
| Frontend-кнопка | `vpn_next/app/(...)` | **Нет** |
| TG-бот integration | `services/payment-service/internal/provider/telegram/...` | **Нет** (Stars-only) |

### 🐛 Критические баги в существующем коде `yoomoney.go`

Это не nitpicks — без починки оплата вообще не пройдёт:

1. **`calculateHash` использует SHA-256, а сравнивает с `sha1_hash`.**
   Строки 232–233:
   ```go
   hash := sha256.Sum256([]byte(data))    // 256 бит
   return hex.EncodeToString(hash[:])     // → 64 hex
   // … но потом сравнивает с values.Get("sha1_hash") — всегда mismatch
   ```
   Реальная подпись от ЮMoney — SHA-1 (40 hex), а с 18 мая 2026 вообще
   уезжает на HMAC-SHA256 в новом поле `sign` (см. §🛠 ниже).

2. **`label` парсится как `payment_{user_id}_{plan_id}_{max_devices}_{ts}`.**
   Это значит, что в БД нет записи `payment` в момент webhook'а — мы её
   _создаём_ из распарсенного label'а. Проблема: теряется контракт «фронт
   создал invoice, ждёт оплату» — pending-row в `payments` нет, история
   платежей в админке не сходится. Должно быть: `label` = `payments.id`
   (uuid), webhook поднимает существующий `pending` payment в `paid`.

3. **Захардкожено `notification_type == "p2p-incoming"`.** Уведомления с
   произвольной карты приходят как `card-incoming` — их код **отбрасывает**.
   Это убивает половину use-case'а (юзер платит с непривязанной карты).

4. **`test_notification` не обрабатывается.** Кнопка «Протестировать» в
   настройках уведомлений ЮMoney пришлёт webhook с `test_notification=true`
   и любым label — текущий код не распарсит label и вернёт ошибку. Соседний
   `card-incoming` плюс отсутствие test-handling делают **админский
   self-test невозможным** до выкатки на прод.

5. **Идемпотентность опирается только на `external_id` UNIQUE,** при этом
   `Invoice.ExternalID = label`, а `WebhookEvent.ExternalID = operation_id`
   (см. строки 90 и 211 в `yoomoney.go`) — это **разные** ID. После
   webhook'а в БД появятся две записи: одна `pending` с `external_id=label`,
   вторая `paid` с `external_id=operation_id`. Reconciliation не работает.

Решение всех пяти багов — в §🛠.

---

## 🏗 Архитектура (без OAuth, MVP)

```
┌──────────────────────────────────────────────────────────────────┐
│  📱 Mini App (vpn_next)                                            │
│                                                                    │
│  1. На /plans — кнопка «Оплатить картой (₽)» рядом со Stars       │
│  2. POST /api/v1/payments → body: {plan_id, max_devices,          │
│                                     provider: "yoomoney"}          │
│  3. В ответе {invoice_link, payment_id} → window.location =       │
│     invoice_link (открываем yoomoney.ru/quickpay в Telegram WebApp│
│     external browser)                                              │
└─────────────────────────────────┬────────────────────────────────┘
                                  │
                                  ▼
┌──────────────────────────────────────────────────────────────────┐
│  💳 yoomoney.ru/quickpay — юзер вводит карту, платит              │
│                                                                    │
│  receiver=<wallet>                                                 │
│  sum=<plan.base_price>                                             │
│  label=<payments.id>      ← UUID, ключ для сопоставления           │
│  successURL=<MINIAPP>/payment/success?id=<payment_id>             │
└─────────────────────────────────┬────────────────────────────────┘
                                  │
                          юзер платит (5–60 сек)
                                  │
                                  ▼
┌──────────────────────────────────────────────────────────────────┐
│  📨 ЮMoney → naш backend (HTTP notification, до 3 ретраев)         │
│                                                                    │
│  POST https://api.osmonai.com/api/v1/payments/webhook/yoomoney    │
│  Content-Type: application/x-www-form-urlencoded                  │
│  notification_type=p2p-incoming | card-incoming                   │
│  operation_id=…                                                    │
│  amount=298.50  ← с учётом комиссии ЮMoney 0.5–3%                 │
│  withdraw_amount=300.00  ← сколько списано у плательщика          │
│  currency=643                                                      │
│  datetime=2026-04-25T12:34:56Z                                    │
│  label=<payments.id>      ← наш UUID                              │
│  sign=<HMAC-SHA256 hex>   ← новая подпись                         │
│  sha1_hash=<sha1 hex>     ← устаревшая, до 18 мая 2026            │
│                                                                    │
│  → Gateway проксирует в payment-service.HandleWebhook              │
│  → YooMoneyProvider.HandleWebhook:                                 │
│    1. Verify sign (HMAC-SHA256 over sorted+url-encoded params)     │
│       Fallback на sha1_hash если sign отсутствует                  │
│    2. Если test_notification=true → 200 OK без действий            │
│    3. SELECT payment WHERE id=<label> AND status='pending'         │
│    4. Сравнить amount c учётом допуска ±3% комиссии                │
│    5. UPDATE status='paid', external_id=operation_id, paid_at=NOW │
│    6. Вызвать subscription.ActivateSub + vpn.CreateVPNUser         │
│    7. Послать сообщение в TG бот юзеру                             │
│  → 200 OK ЮMoney (всегда, даже если внутри упало уже после verify)│
└──────────────────────────────────────────────────────────────────┘
```

**Где НЕ делаем OAuth:** в этом таске. OAuth даст возможность сверять
историю операций через `/api/operation-history` — отдельная задача
(`11-yoomoney-reconciliation.md`), не блокирует приём платежей.

---

## 🛠 Изменения

### Stage 1: Manual setup (один раз руками)

1. **Кошелёк ЮMoney** на админский TG-номер: https://yoomoney.ru/reg
2. **Идентификация кошелька** (паспорт + СНИЛС в приложении). Анонимный =
   лимит остатка 15 000 ₽, упрётесь сразу. Идентифицированный — 500 000 ₽.
3. **Регистрация приложения:** https://yoomoney.ru/myservices/new
   - Название: `Osmon AI`
   - Site URL: `https://cdn.osmonai.com`
   - Email: `admin@osmonai.com` (тот же, что `ACME_EMAIL`)
   - Redirect URI: `https://api.osmonai.com/api/v1/payments/yoomoney/callback`
     (даже без OAuth — заводим сразу, чтоб не пересоздавать на 11-task)
   - `use_oauth2_secret: on`
   - **Сохранить** `client_id` и `client_secret` (понадобятся в task 11)
4. **HTTP-уведомления:** https://yoomoney.ru/transfer/myservices/http-notification
   - URL: `https://api.osmonai.com/api/v1/payments/webhook/yoomoney`
   - Поставить галку «Отправлять HTTP-уведомления»
   - **Сохранить «Секрет»** → это `PAYMENT_YOOMONEY_NOTIFICATION_SECRET`
   - Запросить ФИО/email/phone отправителя — **не ставим** (нам не нужно,
     меньше PII в логах)
   - Жмём «Протестировать» — позже, после деплоя

### Stage 2: ENV-переменные

**`deploy/env/.env.template`:**

```bash
# ------------------------------------------------------------
# YOOMONEY (Quickpay-форма + HTTP-уведомления, без OAuth)
# ------------------------------------------------------------
# Включение провайдера. Если false — yoomoney не регистрируется в
# payment-service, /api/v1/payments?provider=yoomoney вернёт unknown provider.
PAYMENT_YOOMONEY_ENABLED=false

# Номер кошелька получателя (41001XXXXXXXX). Из настроек кошелька.
PAYMENT_YOOMONEY_WALLET=

# Секрет для проверки подписи HTTP-уведомлений.
# Из https://yoomoney.ru/transfer/myservices/http-notification, поле "Секрет".
# НЕ КОММИТИТЬ.
PAYMENT_YOOMONEY_NOTIFICATION_SECRET=

# Куда yoomoney.ru/quickpay редиректит юзера после успешной оплаты.
# Должен быть https и публично доступен. Quickpay не передаёт payment_id —
# фронт берёт его из своего state.
PAYMENT_YOOMONEY_SUCCESS_URL=${MINIAPP_URL}/payment/success

# OAuth credentials — пока НЕ ИСПОЛЬЗУЮТСЯ (см. task 11).
# Заводим сразу, чтобы не возвращаться к настройкам приложения ЮMoney.
PAYMENT_YOOMONEY_CLIENT_ID=
PAYMENT_YOOMONEY_CLIENT_SECRET=
```

**`deploy/env/payment.env.template`:** проброс всех `PAYMENT_YOOMONEY_*` как
обычные `YOOMONEY_*` (см. паттерн `WATA_*` в текущем шаблоне).

### Stage 3: `config.go` — `YooMoneyConfig`

```go
// services/payment-service/internal/config/config.go

type YooMoneyConfig struct {
    Enabled            bool
    Wallet             string  // 41001…
    NotificationSecret string  // Для проверки sign / sha1_hash webhook'ов
    SuccessURL         string  // Куда yoomoney.ru редиректит после оплаты

    // OAuth — не используется в этом таске, см. 11-yoomoney-reconciliation.md
    ClientID     string
    ClientSecret string
}

// + поле YooMoney в struct Config
// + чтение env в New(): YOOMONEY_ENABLED / WALLET / NOTIFICATION_SECRET / SUCCESS_URL
// + Validate(): если Enabled=true → Wallet и NotificationSecret обязательны
```

### Stage 4: Починка `yoomoney.go`

Полная переделка `services/payment-service/internal/provider/yoomoney/yoomoney.go`:

#### 4.1 Конструктор

```go
type YooMoneyProvider struct {
    wallet             string
    notificationSecret string
    successURL         string
    logger             *zap.Logger
}

func NewProvider(wallet, secret, successURL string, logger *zap.Logger) *YooMoneyProvider {
    return &YooMoneyProvider{
        wallet:             wallet,
        notificationSecret: secret,
        successURL:         successURL,
        logger:             logger,
    }
}
```

Лишний `http.Client` убрать — он не используется (Quickpay = просто URL).

#### 4.2 `CreateInvoice`: label = payment_id (UUID), не custom-формат

```go
func (p *YooMoneyProvider) CreateInvoice(ctx context.Context, req *provider.CreateInvoiceRequest) (*provider.Invoice, error) {
    if req.AmountRUB <= 0 {
        return nil, &provider.ProviderError{
            Provider: p.Name(), Code: "invalid_amount",
            Message: fmt.Sprintf("amount_rub must be > 0, got %.2f", req.AmountRUB),
        }
    }

    // Контракт сервиса: вызывающий (payment-service.PaymentService) уже
    // создал row в payments(status='pending') и передал payment_id в
    // req.Metadata["payment_id"]. label = payment_id (UUID).
    paymentID, ok := req.Metadata["payment_id"]
    if !ok || paymentID == "" {
        return nil, &provider.ProviderError{
            Provider: p.Name(), Code: "missing_payment_id",
            Message: "metadata.payment_id is required for yoomoney",
        }
    }

    params := url.Values{}
    params.Set("receiver", p.wallet)
    params.Set("quickpay-form", "shop")
    params.Set("targets", req.Description)
    params.Set("paymentType", "AC")              // AC=карта, PC=кошелёк ЮMoney
    params.Set("sum", fmt.Sprintf("%.2f", req.AmountRUB))
    params.Set("label", paymentID)
    if p.successURL != "" {
        params.Set("successURL", p.successURL+"?id="+paymentID)
    }

    invoiceLink := "https://yoomoney.ru/quickpay/confirm.xml?" + params.Encode()

    return &provider.Invoice{
        ExternalID:  paymentID,                   // в БД external_id == payments.id для pending
        InvoiceLink: invoiceLink,
        Amount:      req.AmountRUB,
        Currency:    "RUB",
        ExpiresAt:   time.Now().Add(24 * time.Hour),
    }, nil
}
```

> ⚠️ Это **меняет контракт `provider.PaymentProvider`** — теперь
> `CreateInvoice` ожидает `Metadata["payment_id"]`. Telegram-провайдер сейчас
> сам генерит `invoice_payload` — придётся либо:
> (a) делать `payment_id` опциональным (yoomoney → required, остальные → нет);
> (b) вынести генерацию `payment_id` на уровень `service.PaymentService`
>     и прокидывать всем провайдерам.
>
> Предпочтительный вариант — **(b)**: уровень service отвечает за БД и ID,
> провайдер — только за внешний API. Такая разводка чище и нужна будет
> для всех будущих провайдеров (ЮKassa, СБП).
>
> Это выходит за рамки именно yoomoney-фикса — есть открытый вопрос ❓1.

#### 4.3 `HandleWebhook`: новая подпись `sign` + fallback `sha1_hash`

```go
func (p *YooMoneyProvider) HandleWebhook(ctx context.Context, payload []byte, _ string) (*provider.WebhookEvent, error) {
    values, err := url.ParseQuery(string(payload))
    if err != nil {
        return nil, providerErr(p, "invalid_payload", "failed to parse form data", err)
    }

    notificationType := values.Get("notification_type")
    if notificationType != "p2p-incoming" && notificationType != "card-incoming" {
        return nil, providerErr(p, "unsupported_notification",
            "unsupported notification type: "+notificationType, nil)
    }

    // 1. Verify signature
    if !verifySign(values, p.notificationSecret) && !verifySha1(values, p.notificationSecret) {
        return nil, providerErr(p, "invalid_signature", "signature verification failed", nil)
    }

    // 2. Test notification → 200 OK без действий
    if values.Get("test_notification") == "true" {
        p.logger.Info("yoomoney test notification received")
        return &provider.WebhookEvent{Status: "test"}, nil
    }

    // 3. Извлечение полей
    operationID := values.Get("operation_id")
    label := values.Get("label")              // payments.id (UUID)
    amount, _ := strconv.ParseFloat(values.Get("amount"), 64)

    if label == "" {
        return nil, providerErr(p, "missing_label", "webhook has no label", nil)
    }

    // 4. Возвращаем событие — service.PaymentService поднимет payment по
    //    label, проверит amount/idempotency, активирует подписку.
    return &provider.WebhookEvent{
        ExternalID: label,                    // ← payments.id, ОДИНАКОВЫЙ для invoice и webhook
        Status:     "paid",
        Metadata: map[string]string{
            "operation_id":      operationID,
            "amount":            values.Get("amount"),
            "withdraw_amount":   values.Get("withdraw_amount"),
            "currency":          values.Get("currency"),
            "datetime":          values.Get("datetime"),
            "sender":            values.Get("sender"),
            "notification_type": notificationType,
        },
    }, nil
}
```

**Внимание:** `WebhookEvent` больше не несёт `UserID/PlanID/MaxDevices`,
потому что они есть в `payments` row. Это влияет на `service.PaymentService.HandleWebhook` — он должен переключиться с «достать UserID из event'а» на «достать row по `external_id` (он же `payment_id`)». Это уже **более чистая** логика, чем сейчас, но требует изменения и в Telegram-обработчике.

#### 4.4 Подписи

```go
// verifySign — новый алгоритм (sign, HMAC-SHA256). Используется с 18 мая 2026.
func verifySign(values url.Values, secret string) bool {
    sign := values.Get("sign")
    if sign == "" {
        return false
    }

    keys := make([]string, 0, len(values))
    for k := range values {
        if k == "sign" {
            continue
        }
        keys = append(keys, k)
    }
    sort.Strings(keys)

    var b strings.Builder
    for i, k := range keys {
        if i > 0 {
            b.WriteByte('&')
        }
        b.WriteString(k)
        b.WriteByte('=')
        b.WriteString(url.QueryEscape(values.Get(k)))
    }

    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(b.String()))
    expected := hex.EncodeToString(mac.Sum(nil))

    return subtle.ConstantTimeCompare([]byte(expected), []byte(sign)) == 1
}

// verifySha1 — устаревший алгоритм (sha1_hash). Перестанет приходить с 18 мая 2026.
func verifySha1(values url.Values, secret string) bool {
    sha1Hash := values.Get("sha1_hash")
    if sha1Hash == "" {
        return false
    }
    parts := []string{
        values.Get("notification_type"),
        values.Get("operation_id"),
        values.Get("amount"),
        values.Get("currency"),
        values.Get("datetime"),
        values.Get("sender"),
        values.Get("codepro"),
        secret,
        values.Get("label"),
    }
    sum := sha1.Sum([]byte(strings.Join(parts, "&")))
    expected := hex.EncodeToString(sum[:])
    return subtle.ConstantTimeCompare([]byte(expected), []byte(sha1Hash)) == 1
}
```

После 18 мая 2026 удаляем `verifySha1` целиком.

### Stage 5: Wiring в `app.go`

Раскомментировать блок 132–144 + замена на новый конструктор:

```go
// services/payment-service/internal/app/app.go § initProviders()

if a.config.YooMoney.Enabled {
    yoomoneyProvider := yoomoney.NewProvider(
        a.config.YooMoney.Wallet,
        a.config.YooMoney.NotificationSecret,
        a.config.YooMoney.SuccessURL,
        a.logger,
    )
    providers = append(providers, yoomoneyProvider)
    a.logger.Info("YooMoney provider initialized",
        zap.String("wallet", a.config.YooMoney.Wallet))
}
```

### Stage 6: Изменения в `service.PaymentService`

Чтобы `WebhookEvent` без `UserID/PlanID/MaxDevices` работал — изменить
`service.HandleWebhook`:

```go
// services/payment-service/internal/service/payment.go

func (s *PaymentService) HandleWebhook(ctx context.Context, providerName string, payload []byte, signature string) (*pb.HandleWebhookResponse, error) {
    p := s.providerByName(providerName)
    event, err := p.HandleWebhook(ctx, payload, signature)
    if err != nil {
        return nil, err
    }
    if event.Status == "test" {
        return &pb.HandleWebhookResponse{Status: "test"}, nil
    }

    // Идемпотентный поиск payment по external_id (= payments.id для yoomoney,
    // = telegram_payment_charge_id для Stars).
    payment, err := s.repo.FindByExternalID(ctx, event.ExternalID)
    if err != nil { ... }
    if payment == nil {
        // Stars-flow создаёт row in-flight (event.UserID/PlanID есть)
        if event.UserID != 0 {
            payment, err = s.repo.CreateFromWebhook(ctx, event)
        } else {
            return nil, ErrUnknownPayment
        }
    }
    if payment.Status == "paid" {
        return &pb.HandleWebhookResponse{Status: "ok"}, nil  // дубль, ничего не делаем
    }

    // Сверка amount с допуском (для yoomoney — комиссия 0.5–3%)
    if !s.amountMatches(payment, event) { ... }

    // UPDATE status='paid'
    err = s.repo.MarkPaid(ctx, payment.ID, event.Metadata["operation_id"])
    if err != nil { ... }

    // Активация подписки + VPN-юзер
    err = s.activateSubscription(ctx, payment)
    if err != nil { ... }

    return &pb.HandleWebhookResponse{Status: "ok"}, nil
}
```

Допуск на `amount`:

```go
// для yoomoney: ЮMoney удерживает 0.5% (с кошелька) до 3% (с карты).
// withdraw_amount (если есть) = сколько списано у юзера. Сверяем с ним:
const yoomoneyTolerance = 0.005     // 0.5% — наш порог shortfall'а
expected := payment.AmountRub
got := event.WithdrawAmount        // парсим из metadata
if got < expected*(1-yoomoneyTolerance) {
    return ErrAmountMismatch
}
```

Вариант с `withdraw_amount` лучше, но он есть только при HTTPS-уведомлениях
(см. §🛠.1). У нас всегда HTTPS → ок.

### Stage 7: Frontend (`vpn_next`)

1. **`/plans`:** на каждой карточке две кнопки — «Telegram Stars» и «Картой ₽».
   Вторая → `POST /api/v1/payments {plan_id, max_devices, provider: "yoomoney"}`.
2. В ответе `{invoice_link, payment_id}`. Открываем `invoice_link` через
   `window.open(...)` (Telegram WebApp выдаст внешний браузер).
3. `payment_id` сохраняем в `localStorage` → нужен для page `/payment/success`.
4. **`/payment/success`:** polling `GET /api/v1/payments/{id}` каждые 2 сек.
   - `status='paid'` → «✅ Оплата прошла, подписка активна» + ссылка на VPN
   - `status='pending'` через 3 минуты → «⏳ платёж ещё обрабатывается, мы пришлём уведомление в бот»
   - `status='failed'/'cancelled'` → «❌ что-то пошло не так, [написать саппорту]»

### Stage 8: Тесты

- **Unit (`yoomoney_test.go`):**
  - `verifySign` на эталонном примере из доки (есть в `yoomoney-api-reference.md` § 1.10)
  - `verifySha1` на нашем формате
  - `HandleWebhook` с `test_notification=true` → `Status="test"`, `error=nil`
  - `HandleWebhook` с `card-incoming` (а не только `p2p-incoming`)
  - `CreateInvoice` без `Metadata["payment_id"]` → `missing_payment_id`
- **Integration:**
  - Поднять локальный бот, симулировать payment с реальной 1₽-оплатой
  - Засабмитить тест из админки ЮMoney «Протестировать» → backend пишет
    `yoomoney test notification received` в logs, БД не трогает

---

## ✅ Definition of Done

- [ ] **Manual setup:** кошелёк идентифицирован, приложение зарегано на
      myservices/new, HTTP-уведомления включены, секреты в `1Password` (или
      где у нас лежат)
- [ ] **`.env`:** добавлены 6 переменных `PAYMENT_YOOMONEY_*`, прокинуты в
      `payment.env.template`
- [ ] **Config:** `YooMoneyConfig` struct + чтение env + Validate()
- [ ] **`yoomoney.go`:** все 5 багов из §📚 пофикшены; есть `verifySign` и
      `verifySha1`; `card-incoming` обрабатывается; `test_notification`
      возвращает status="test"
- [ ] **Контракт `WebhookEvent`:** lookup по `external_id` (=`payments.id`),
      Stars-провайдер сохраняет совместимость
- [ ] **Wiring:** `app.go` раскомментирован, провайдер регистрируется, если
      `PAYMENT_YOOMONEY_ENABLED=true`
- [ ] **Frontend:** на `/plans` кнопка «Картой ₽», открывает invoice_link;
      `/payment/success` поллит статус
- [ ] **Тесты:** unit для `verifySign`/`verifySha1`/`HandleWebhook` зелёные
- [ ] **Smoke:** реальная оплата 1₽ с другого кошелька → подписка активна
      → юзеру в TG бот сообщение
- [ ] **Smoke:** «Протестировать» в настройках уведомлений ЮMoney → 200 OK,
      `test notification received` в логах, БД не тронута
- [ ] **Безопасность:** webhook handler НЕ требует JWT (это публичная
      ручка), но ВСЕГДА проверяет `sign`/`sha1_hash`; mismatch → 403 без
      детального лога (вероятный злоумышленник)
- [ ] **Доки:** обновить `services/yoomoney-integration.md` §«Статус
      реализации» с 📋 на ✅

---

## ⚠️ Риски и ограничения

| Риск | Митигация |
|---|---|
| Юзер закроет браузер до показа successURL | Не страшно — webhook прилетит независимо. Подписка активируется, фронту юзера не нужно знать |
| Webhook не дошёл (3 ретрая ЮMoney проебались) | Запись в истории кошелька осталась. **Ручной reconciliation через task 11 (OAuth + operation-history)**. До этого — админ может в БД руками `UPDATE payments SET status='paid'` + триггерить активацию |
| Юзер заплатил **меньше** (комиссия больше нашего допуска) | Webhook сравнивает `withdraw_amount` (списано у юзера) с `payment.amount_rub` ± 0.5%. Если меньше → status='failed', саппорт-кейс |
| Юзер заплатил **больше** (опечатался в сумме) | Quickpay по умолчанию даёт юзеру **редактировать сумму** в форме. Если хочется лишить юзера этой возможности — добавить `quickpay-form=button` или `formcomment=true`, см. https://yoomoney.ru/docs/payment-buttons. **MVP: разрешаем, лишку зачисляем как «бонус» (ручной credit на следующую подписку через саппорт)** |
| Лимит остатка кошелька | Идентифицированный — 500k₽. Регулярно выводить на банковский счёт (cron-напоминание раз в неделю) |
| Лимит оборота физлица | Формально не лимитирован у идентифицированного, но при больших объёмах ЮMoney может попросить документы. Если оборот >100k₽/мес — тянуть переход на ЮKassa (юрлицо/ИП), task TBD |
| `sha1_hash` deprecation 18 мая 2026 | Уже сразу пишем `verifySign`-first, `verifySha1` как fallback. Удаляем fallback после 18 мая 2026 (поставить cron-напоминание) |
| Sandbox у ЮMoney нет | Тестируем 1₽-переводом. Это не страшно (1₽ × N тестов = копейки) |

---

## ✅ Принятые решения

1. **`payments.id` остаётся `BIGSERIAL`. Service сначала INSERT row, потом
   CreateInvoice.** Это требует рефакторинга `service.PaymentService.CreatePayment`:
   - **До:** `provider.CreateInvoice → INSERT payments(external_id=invoice.ExternalID)`
   - **После:** `INSERT payments(status='pending', external_id=NULL) → provider.CreateInvoice(metadata.payment_id=row.id) → UPDATE external_id=invoice.ExternalID`
   - Telegram Stars-провайдер сохраняет совместимость: для него `metadata.payment_id` опционален, `invoice_payload` остаётся как сейчас.
   - YooMoney-провайдер требует `metadata.payment_id` обязательным и кладёт его в `label` quickpay-формы. После webhook'а lookup идёт по `payments.id == label`.

2. **UX: одна кнопка на карточке плана → модалка «Способ оплаты».**
   - На `/plans` каждая карточка имеет одну CTA «Купить».
   - Клик → модалка с двумя опциями: «Telegram Stars» и «Картой ₽» (для yoomoney).
   - Если включён только один провайдер — модалка пропускается, идёт прямой переход.
   - Чище визуально, расширяемо для будущих провайдеров (ЮKassa, СБП).

3. **Rollout: сразу на всех юзеров.** `PAYMENT_YOOMONEY_ENABLED=true` в проде после smoke-теста с 1 ₽. Feature-flag по `telegram_id` не делаем — комплексность не оправдана для MVP, реальная защита — это качество кода и smoke-тест перед выкаткой.

4. **При недоплате (`withdraw_amount` < `amount_rub * 0.95`):** payment → `failed`, юзеру в TG бот — «оплата не прошла, обратитесь в саппорт». Не активируем частичную подписку, не делаем pro-rate. Если случай реален (большая комиссия) — саппорт ручкой делает `UPDATE payments SET status='paid'` и активирует подписку.

5. **`card-incoming` обрабатываем так же, как `p2p-incoming`.** Различие только в `sender` (для card — пустая строка) и в наличии контактных данных. Для нашего use-case разницы нет. На smoke-тесте подтвердим, что quickpay-оплата с карты приходит как `p2p-incoming` (карта внутри ЮMoney мгновенно превращается во временный кошелёк-источник).

---

## 🔗 Ссылки

- Родительский: [services/yoomoney-integration.md](../services/yoomoney-integration.md)
- API-референс: [services/yoomoney-api-reference.md](../services/yoomoney-api-reference.md)
- Smoke-test инструкция оплаты 1 ₽: TBD после деплоя
- Будущая задача 11: «ЮMoney OAuth + reconciliation через operation-history»
  (нужна админка для сверки потерянных webhook'ов)
