# YooMoney Integration — приём переводов на кошелёк проекта

Как подключить ЮMoney API для приёма переводов на единый кошелёк проекта. Используется **Вариант A**: OAuth-редирект приземляется на backend, backend сам обменивает `code` на `access_token` и сохраняет его.

**Статус реализации:** 📋 **план** (не реализовано, но параметры уже зарегистрированы)

> 📚 Полный API-референс ЮMoney по всем 22 страницам docs (auth, format, account-info,
> history, payments, webhook, showcase) — в [yoomoney-api-reference.md](./yoomoney-api-reference.md).
> Эта страница описывает только **наш** план интеграции; общие подробности и поля API
> там.

---

## 🧠 Простыми словами

У нас уже есть оплата через **Telegram Stars** (см. [payment-integration.md](./payment-integration.md)). ЮMoney — это **дополнительный** канал для приёма переводов на рублёвый кошелёк проекта:

1. Админ **один раз** авторизуется в ЮMoney через OAuth2 и даёт приложению доступ к кошельку
2. ЮMoney возвращает `access_token` — он сохраняется в БД навсегда (refresh не требуется, токен ЮMoney долгоживущий)
3. Когда юзер делает перевод на кошелёк проекта, ЮMoney шлёт **HTTP-уведомление** на `notification_uri`
4. Backend проверяет sha1-подпись уведомления, сопоставляет по `label` (наш `payment_id`) и активирует подписку

Это **не per-user OAuth** — юзеры ничего не авторизуют, они просто переводят по реквизитам/через форму `quickpay`. OAuth нужен только админу, чтобы сервер мог читать операции через API ЮMoney.

---

## 📋 Параметры приложения ЮMoney

Зарегистрировано на https://yoomoney.ru/myservices/new

```json
{
  "app_name": "Osmon AI",
  "site_url": "https://cdn.osmonai.com",
  "email": "admin@osmonai.com",
  "redirect_uri": "https://api.osmonai.com/api/v1/payments/yoomoney/callback",
  "notification_uri": "https://api.osmonai.com/api/v1/payments/yoomoney/webhook",
  "use_oauth2_secret": "on"
}
```

### Почему такие значения

| Поле | Значение | Обоснование |
|---|---|---|
| `app_name` | `Osmon AI` | Показывается юзеру на экране OAuth-согласия ЮMoney |
| `site_url` | `https://cdn.osmonai.com` | Домен Mini App (`CDN_DOMAIN` в `.env`). Чисто информационный линк |
| `email` | `admin@osmonai.com` | Тот же, что `ACME_EMAIL` — реальный ящик админа, сюда ЮMoney шлёт уведомления по приложению |
| `redirect_uri` | `https://api.osmonai.com/api/v1/payments/yoomoney/callback` | Backend-ручка на `API_DOMAIN`. Сюда ЮMoney возвращает юзера с `?code=…` после OAuth. **Должен совпадать точно** с `redirect_uri` в запросе `/oauth/authorize` |
| `notification_uri` | `https://api.osmonai.com/api/v1/payments/yoomoney/webhook` | Backend-ручка для HTTP-уведомлений о входящих переводах. Защищена проверкой sha1-подписи |
| `use_oauth2_secret` | `on` | Confidential OAuth2-клиент — для обмена `code → access_token` требуется `client_secret`. Обязательно для backend-приложений |

### Секреты, которые ЮMoney выдаёт после регистрации

После сабмита формы на `yoomoney.ru/myservices/new` ЮMoney покажет:

- `client_id` — публичный, кладём в `YOOMONEY_CLIENT_ID`
- `client_secret` — приватный, кладём в `YOOMONEY_CLIENT_SECRET`, **никогда не коммитим**
- `notification_secret` — для проверки sha1-подписи HTTP-уведомлений, кладём в `YOOMONEY_NOTIFICATION_SECRET`

---

## 🏗️ Архитектура (Вариант A)

### 1. Разовая OAuth-авторизация кошелька

```
┌──────────────────────────────────────────────────────────────────────┐
│  👤 Админ (браузер)                                                   │
│                                                                       │
│  1. GET https://api.osmonai.com/api/v1/payments/yoomoney/connect     │
│     (CLI-команда task yoomoney:connect ИЛИ админка)                  │
│                                                                       │
│  2. Backend → 302 → https://yoomoney.ru/oauth/authorize?             │
│         client_id=…                                                   │
│        &response_type=code                                            │
│        &redirect_uri=https://api.osmonai.com/api/v1/payments/        │
│                     yoomoney/callback                                 │
│        &scope=account-info operation-history operation-details       │
│                                                                       │
│  3. Админ входит в ЮMoney, жмёт "Разрешить"                          │
│                                                                       │
│  4. ЮMoney → 302 → https://api.osmonai.com/.../callback?code=XXX     │
└───────────────────────────────────────┬──────────────────────────────┘
                                        │
                                        ▼
┌──────────────────────────────────────────────────────────────────────┐
│  ⚙️  Gateway → payment-service (gRPC)                                  │
│                                                                       │
│  5. POST https://yoomoney.ru/oauth/token                              │
│     code=XXX                                                          │
│     client_id=…                                                       │
│     client_secret=… ◄── потому что use_oauth2_secret=on               │
│     grant_type=authorization_code                                     │
│     redirect_uri=… (тот же!)                                          │
│                                                                       │
│  6. ← access_token (долгоживущий)                                     │
│                                                                       │
│  7. INSERT INTO yoomoney_credentials (access_token, granted_at)       │
│                                                                       │
│  8. 302 → https://cdn.osmonai.com/admin/payments?yoomoney=connected  │
└──────────────────────────────────────────────────────────────────────┘
```

### 2. Приём перевода от юзера

```
┌──────────────────────────────────────────────────────────────────────┐
│  📱 Mini App                                                           │
│                                                                       │
│  1. "Оплатить через ЮMoney" → POST /api/v1/payments/yoomoney         │
│     body: {plan_id, max_devices}                                     │
│     → backend создаёт payment (status=pending),                       │
│        генерит quickpay-URL с label=<payment_id>                      │
│                                                                       │
│  2. Открываем https://yoomoney.ru/quickpay/confirm.xml?...            │
│     receiver=<wallet>                                                 │
│     sum=<amount>                                                      │
│     label=<payment_id>      ← КЛЮЧ для сопоставления                  │
│     targets=Osmon AI подписка                                         │
│     successURL=https://cdn.osmonai.com/payments/success               │
└──────────────────────────────────────────────┬───────────────────────┘
                                               │
                                    юзер платит
                                               │
                                               ▼
┌──────────────────────────────────────────────────────────────────────┐
│  📨 ЮMoney → наш backend (HTTP notification)                           │
│                                                                       │
│  POST https://api.osmonai.com/api/v1/payments/yoomoney/webhook        │
│     Content-Type: application/x-www-form-urlencoded                   │
│     notification_type=p2p-incoming                                    │
│     operation_id=…                                                    │
│     amount=299.00                                                     │
│     currency=643                                                      │
│     datetime=2026-04-24T12:34:56Z                                     │
│     sender=…                                                          │
│     codepro=false                                                     │
│     label=<payment_id>      ← наш ID                                  │
│     sha1_hash=<подпись>                                               │
│                                                                       │
│  3. Проверяем sha1:                                                   │
│     sha1(notification_type&operation_id&amount&currency&datetime&     │
│          sender&codepro&NOTIFICATION_SECRET&label) == sha1_hash       │
│                                                                       │
│  4. SELECT payment WHERE id=<label> AND status=pending                │
│     amount совпадает? → UPDATE status=succeeded                       │
│                        → call subscription-service.ActivateSub        │
│                                                                       │
│  5. 200 OK (обязательно, иначе ЮMoney ретраит)                        │
└──────────────────────────────────────────────────────────────────────┘
```

---

## 🔐 Проверка подписи HTTP-уведомлений

> ⚠️ **Важное изменение:** старая подпись `sha1_hash` устарела и **перестанет
> приходить с 18 мая 2026 года**. С момента старта реализации сразу пишем
> код под новую подпись `sign` (HMAC-SHA256). Старый `sha1_hash` пока всё ещё
> приходит, но валидируем его только как fallback (или не валидируем вовсе,
> если код мержим уже после дедлайна).

### Алгоритм `sign` (актуальный, HMAC-SHA256)

`sign` — это HMAC-SHA256 в HEX (lowercase) от URL-кодированной строки
параметров уведомления, отсортированных по алфавиту, **без** самого `sign`.

Шаги:

1. Извлечь все поля тела webhook.
2. Удалить из множества поле `sign`.
3. Отсортировать оставшиеся поля по имени по алфавиту (A-Z).
4. URL-кодировать каждое значение (UTF-8, RFC 3986). Пустые значения остаются как `key=`.
5. Соединить в строку формата `key=value`, разделитель — `&`.
6. Посчитать `HMAC-SHA256(secret, signing_string)`, представить в HEX (lowercase).
7. Сравнить со значением `sign` в **constant time**.

`secret` — `YOOMONEY_NOTIFICATION_SECRET` из настроек кошелька
(https://yoomoney.ru/transfer/myservices/http-notification, поле «Секрет»).

Псевдокод проверки на Go:

```go
func verifyYoomoneyNotification(form url.Values, secret string) bool {
    keys := make([]string, 0, len(form))
    for k := range form {
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
        b.WriteString(url.QueryEscape(form.Get(k)))
    }

    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(b.String()))
    expected := hex.EncodeToString(mac.Sum(nil))

    return subtle.ConstantTimeCompare([]byte(expected), []byte(form.Get("sign"))) == 1
}
```

> ⚠️ ЮMoney в примерах URL-кодирует значения **точно так же**, как Go's
> `url.QueryEscape`: пробел → `%20` через перекодирование (не `+`). Если
> подпись не сходится — первое, что проверять, кодировку русских строк
> (UTF-8) и формат пробелов.

### Алгоритм `sha1_hash` (устаревший, до 18 мая 2026)

Оставляю для случая, если интеграцию мержим до дедлайна и хотим dual-check:

```
sha1_hash = sha1(
  notification_type & operation_id & amount & currency & datetime &
  sender & codepro & NOTIFICATION_SECRET & label
)
```

Где `&` — это **литеральный амперсанд** между значениями (не URL-разделитель).
После 18 мая 2026 поле `sha1_hash` в webhook'ах вообще не будет приходить —
весь `sha1`-код можно удалять.

### Семантика ответа на webhook

- Подпись не сошлась → `403 Forbidden`, не логируем как `error` (это почти
  наверняка злоумышленник).
- Подпись сошлась, но `payment_id` (из `label`) не найден или `amount`
  не совпадает → `200 OK` + лог `warning`. ЮMoney не будет ретраить, а
  значит ошибка наша и разбираемся ручками.
- Любой **реальный** (успешно обработанный) webhook → `200 OK`, обработка
  **идемпотентная** — ЮMoney делает 3 попытки доставки (сразу, через 10
  минут, через 1 час), при ретрае придёт дубль.

---

## 🌍 ENV-переменные

Добавить в `deploy/env/.env.template`:

```bash
# ------------------------------------------------------------
# YOOMONEY (доп. канал оплаты, этап TBD)
# ------------------------------------------------------------
# Приложение: https://yoomoney.ru/myservices/new
# app_name=Osmon AI, redirect_uri=https://${API_DOMAIN}/api/v1/payments/yoomoney/callback
YOOMONEY_CLIENT_ID=<выдаётся ЮMoney после регистрации>
YOOMONEY_CLIENT_SECRET=<выдаётся ЮMoney, НЕ КОММИТИТЬ>
YOOMONEY_NOTIFICATION_SECRET=<Настройки приложения → HTTP-уведомления → "Секрет">
YOOMONEY_WALLET=<номер кошелька 41001XXXXXXXXXX для quickpay receiver>
YOOMONEY_REDIRECT_URI=https://${API_DOMAIN}/api/v1/payments/yoomoney/callback
YOOMONEY_SUCCESS_URL=https://${CDN_DOMAIN}/payments/success
YOOMONEY_SCOPES=account-info operation-history operation-details
```

И проксировать в `deploy/env/payment.env.template`:

```bash
YOOMONEY_CLIENT_ID=${YOOMONEY_CLIENT_ID}
YOOMONEY_CLIENT_SECRET=${YOOMONEY_CLIENT_SECRET}
YOOMONEY_NOTIFICATION_SECRET=${YOOMONEY_NOTIFICATION_SECRET}
YOOMONEY_WALLET=${YOOMONEY_WALLET}
YOOMONEY_REDIRECT_URI=${YOOMONEY_REDIRECT_URI}
YOOMONEY_SUCCESS_URL=${YOOMONEY_SUCCESS_URL}
YOOMONEY_SCOPES=${YOOMONEY_SCOPES}
```

---

## 🧩 API endpoints (план)

Все приземляются на Gateway (`api.osmonai.com`) и проксируются в `payment-service`.

| Метод | Путь | Защита | Назначение |
|---|---|---|---|
| `GET` | `/api/v1/payments/yoomoney/connect` | admin JWT | 302 на `yoomoney.ru/oauth/authorize` с нашим `client_id` |
| `GET` | `/api/v1/payments/yoomoney/callback` | — (публично) | Приёмник OAuth `?code=…`, обменивает на `access_token`, сохраняет, 302 на `/admin/payments?yoomoney=connected` |
| `POST` | `/api/v1/payments/yoomoney` | user JWT | Создаёт payment, возвращает `quickpay_url` + `payment_id` |
| `POST` | `/api/v1/payments/yoomoney/webhook` | sha1-подпись | HTTP-уведомление от ЮMoney о поступлении перевода |

---

## 🗄️ Схема БД (план)

```sql
-- Одна строка, в которой лежит OAuth-токен кошелька проекта.
CREATE TABLE yoomoney_credentials (
    id              SERIAL PRIMARY KEY,
    access_token    TEXT NOT NULL,
    scope           TEXT NOT NULL,
    granted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at      TIMESTAMPTZ
);

-- Уже существующая таблица payments расширяется полем provider='yoomoney'.
-- external_id = operation_id из webhook, label = payments.id.
-- Идемпотентность: UNIQUE (provider, external_id).
```

---

## ⚠️ Подводные камни

1. **`redirect_uri` — буква в букву.** Если в форме регистрации написал `.../callback`, а в запросе `/oauth/authorize` передал `.../callback/` (лишний слеш) — ЮMoney вернёт `redirect_uri_mismatch`.
2. **Только HTTPS.** Caddy уже выпускает серт на `api.osmonai.com` через auto-TLS, см. [04-caddy-auto-tls.md](../tasks/04-caddy-auto-tls.md).
3. **`label` — это наш `payment_id`**, иначе не сможем сопоставить webhook с записью в БД. Юзер не может его изменить, т.к. мы формируем quickpay-URL на backend'е.
4. **ЮMoney ретраит webhook'и** при не-200 ответе. Всегда отвечаем `200` если подпись валидна, даже если обработка внутри упала — иначе получим шторм ретраев.
5. **`amount` в webhook — это то, что **получил кошелёк**, уже за вычетом комиссии ЮMoney (~0.5–3%).** Если юзер должен заплатить 299 ₽, нам придёт меньше. Либо закладываем комиссию в тариф, либо сверяем `withdraw_amount` вместо `amount`.
6. **`access_token` у ЮMoney долгоживущий** (годы), но может быть отозван юзером в настройках. На 401 от API → в лог `error` + дисейблим канал оплаты + алерт админу.
7. **`use_oauth2_secret=on`** → при обмене `code → token` **обязательно** передавать `client_secret`. Если забыл — ЮMoney вернёт `invalid_client`.
8. **Тестовая среда.** У ЮMoney нет отдельного sandbox'а под API переводов — тестируем переводом реальных ~1 ₽ с другого кошелька.

---

## 🔗 Ссылки

- Регистрация приложения: https://yoomoney.ru/myservices/new
- Документация API: https://yoomoney.ru/docs/wallet
- HTTP-уведомления: https://yoomoney.ru/docs/wallet/using-api/notification-p2p-incoming
- Quickpay-форма: https://yoomoney.ru/docs/payment-buttons
