# YooMoney Wallet API — полный справочник

> Конспект всех 22 страниц официальной документации https://yoomoney.ru/docs/wallet
> (по состоянию на апрель 2026). Используется как «карманный» референс при разработке
> [yoomoney-integration.md](./yoomoney-integration.md).
>
> Структура повторяет структуру оригинальной доки (sidebar) в том же порядке, что
> на сайте ЮMoney.

---

## ⚠️ Краткие замечания, которые легко проебать

1. **С 17 марта 2020 года — только TLS 1.2+.** На любой HTTPS-вызов к
   `yoomoney.ru` (OAuth, API, webhook от ЮMoney к нам).
2. **`sha1_hash` в HTTP-уведомлениях устарел и перестанет приходить с 18 мая
   2026 года.** Новая подпись — `sign` (HMAC-SHA256 в HEX от URL-кодированной
   строки **отсортированных по алфавиту** параметров, кроме самого `sign`).
   См. раздел [Уведомление о входящем переводе](#уведомление-о-входящем-переводе-notification-p2p-incoming).
3. **`access_token` действует 3 года** (для токенов, выданных после 7 февраля
   2018 года; до этой даты — 6 месяцев). Refresh не предусмотрен — после
   протухания нужно прогнать OAuth заново.
4. **Кошелёк ≠ ЮKassa.** Это **API кошелька физлица**: личный счёт `41001…`,
   а не магазинный. Для бизнеса (юрлица/ИП) — отдельный продукт ЮKassa
   (https://yookassa.ru).
5. **Один `client_id` — одна авторизация на пользователя.** Повторный
   `/oauth/authorize` тем же `client_id` **аннулирует** предыдущий токен.
   Для нескольких токенов на одного пользователя — параметр `instance_name`.
6. **Все API-методы → `POST application/x-www-form-urlencoded`.** Несмотря
   на JSON в ответах, запросы — `form-urlencoded`. Никаких `application/json`.
7. **Webhook ретраится 3 раза:** сразу, через 10 минут, через 1 час. После
   третьей неудачи запись остаётся только в истории кошелька — иначе никак.
8. **Контактные данные отправителя (ФИО/email/phone/адрес) приходят в webhook
   только если `notification_uri` использует HTTPS.** На HTTP всё это
   обнуляется до пустых строк, даже если запрашивалось.

---

## 0. Общие сведения

API кошелька (https://yoomoney.ru/docs/wallet) позволяет частным лицам
программно работать со своим кошельком ЮMoney:

- **получать и отправлять переводы**, делать платежи с банковской карты или из
  кошелька;
- **запрашивать баланс и историю** операций;
- **получать HTTP-уведомления** о входящих переводах.

Если нужно принимать платежи на расчётный счёт **юрлица или ИП** — это
другой продукт, **ЮKassa** (https://yookassa.ru), не описывается тут.

Состоит из четырёх крупных блоков:

1. [Авторизация и протокол](#1-авторизация-и-описание-протокола)
2. [Информация о счёте](#2-информация-о-счёте-пользователя)
3. [Платежи из кошелька](#3-платежи-из-кошелька)
4. [Формы оплаты товаров и услуг (showcase)](#4-формы-оплаты-товаров-и-услуг-showcase)

---

## 1. Авторизация и описание протокола

### 1.1 Общее описание (OAuth2)

Авторизация по [RFC 6749 (OAuth 2.0)](https://tools.ietf.org/html/rfc6749) +
[RFC 6750 (Bearer Token)](https://tools.ietf.org/html/rfc6750).

Полный пользовательский сценарий:

1. Разработчик регистрирует приложение → получает `client_id` (+ опционально
   `client_secret`).
2. Приложение делает `POST /oauth/authorize` с `client_id`, `response_type=code`,
   `redirect_uri`, `scope`. **Запрещается** открывать эту страницу прямо
   из приложения — только через браузер ОС, потому что логин/пароль ЮMoney
   юзер должен вводить на странице ЮMoney, а не в приложении.
3. ЮMoney → 302 на свою страницу логина. Юзер вводит пароль, видит список
   `scope`, жмёт «Разрешить» / «Отклонить».
4. ЮMoney → 302 на `redirect_uri` с `?code=…` (или `?error=…`).
5. Приложение **немедленно** обменивает `code` на `access_token` через
   `POST /oauth/token` (время жизни code < 1 мин).
6. Полученный `access_token` — симметричный секрет приложения, права —
   те, что в `scope`.

**Безопасность (требования протокола):**

1. Только HTTPS. TLS ≥ 1.2.
2. Проверять SSL-сертификат сервера, прерывать сессию при невалидном.
3. Никогда не хранить токен в открытом виде, в том числе в cookies.
4. Никогда не передавать токен в GET/POST-параметрах (только в заголовке
   `Authorization: Bearer …`).
5. `client_secret` никогда не должен проходить через устройство юзера/браузер
   (только server-side обмен `code → token`).
6. `client_secret` используется только в `/oauth/token`, нигде больше.

### 1.2 Регистрация приложения

URL формы: https://yoomoney.ru/myservices/new (нужен залогиненный кошелёк
ЮMoney; нет кошелька — https://yoomoney.ru/reg).

Поля формы:

| Поле | Описание |
|---|---|
| Название для пользователей | Видно юзеру на экране OAuth-согласия |
| Адрес сайта | Линк на сайт приложения/разработчика |
| Почта для связи | Email админа |
| Redirect URI | Точный URI приземления OAuth (см. RFC 6749 `redirect_uri`) |
| Логотип | Картинка приложения |
| Проверять подлинность приложения (`use_oauth2_secret`) | Если включено — выдадут `client_secret`. Для backend-приложений — **обязательно on** |

После сабмита показывают:

- `client_id` — публичный
- `client_secret` — приватный (только если включена опция выше)
- (отдельно, на странице настроек уведомлений) `notification_secret` — секрет
  для подписи HTTP-уведомлений

> Утечка `client_id` без `client_secret` → возможны фишинговые атаки от вашего
> имени. Поэтому **`client_secret` обязателен**, и его нельзя хранить ни на
> устройстве юзера, ни в публичных репах.

### 1.3 Запрос авторизации (`/oauth/authorize`)

```http
POST /oauth/authorize HTTP/1.1
Host: yoomoney.ru
Content-Type: application/x-www-form-urlencoded

client_id=<client_id>
&response_type=code
&redirect_uri=<redirect_uri>
&scope=<scope>
&instance_name=<instance_name>          # опционально
```

| Параметр | Тип | Назначение |
|---|---|---|
| `client_id` | string | Из регистрации |
| `response_type` | string | Только `code` |
| `redirect_uri` | string | **Посимвольно** идентичен указанному при регистрации (можно дописывать query-параметры в конец, они в сравнении не учитываются) |
| `scope` | string | Список прав через пробел, регистрозависимы |
| `instance_name` | string | Опционально. Уникальный ID юзера в нашем приложении (например, его логин). Позволяет иметь **несколько токенов одного `client_id` на одного человека** |

Ответ — `302 Found Location: <redirect_uri>?code=…` или `?error=…`.

**Возможные `error`:**

| Код | Описание | Поведение |
|---|---|---|
| `invalid_request` | Нет/некорректные обязательные параметры | Страница с текстом ошибки на yoomoney.ru |
| `invalid_scope` | `scope` отсутствует/некорректен/имеет логические противоречия | Страница с ошибкой |
| `unauthorized_client` | Неверный `client_id` или `client_id` заблокирован ЮMoney | Страница с ошибкой |
| `access_denied` | Юзер отклонил запрос | 302 на `redirect_uri?error=access_denied` |

Время жизни `code` — **< 1 минуты**, использовать строго один раз.

### 1.4 Получение токена (`/oauth/token`)

```http
POST /oauth/token HTTP/1.1
Host: yoomoney.ru
Content-Type: application/x-www-form-urlencoded

code=<code>
&client_id=<client_id>
&grant_type=authorization_code
&redirect_uri=<redirect_uri>            # тот же, что в /authorize
&client_secret=<client_secret>          # если приложение confidential
```

Ответ — JSON:

```json
{ "access_token": "410012345678901.0123…" }
```

Или (HTTP 4xx):

```json
{ "error": "invalid_grant" }
```

**Возможные `error`:**

| Код | Описание |
|---|---|
| `invalid_request` | Нет/некорректные параметры |
| `unauthorized_client` | Неверный `client_id`/`client_secret`, либо `client_id` заблокирован |
| `invalid_grant` | Code не выдавался / просрочен / уже обменен |

**Срок жизни `access_token`:** 3 года (для выданных после 7 февраля 2018);
6 месяцев — для более старых.

Хранить токен зашифрованным (например, 3DES + 4-значный пин юзера).

### 1.5 Отзыв токена (`POST /api/revoke`)

```http
POST /api/revoke HTTP/1.1
Host: yoomoney.ru
Authorization: Bearer <access_token>
Content-Length: 0
```

| HTTP | Что значит |
|---|---|
| `200 OK` | Отозван |
| `400 Bad Request` | Запрос не парсится / нет Authorization |
| `401 Unauthorized` | Токен не существует или уже отозван |

### 1.6 Права на выполнение операций (scope)

Права в `scope` через пробел, регистрозависимы.

#### Базовые права

| Право | Описание |
|---|---|
| `account-info` | `/api/account-info` (баланс, статус) |
| `operation-history` | `/api/operation-history` |
| `operation-details` | `/api/operation-details` (детали отдельной операции) |
| `payment` | Платежи в **конкретный** магазин или на **конкретный** счёт (с ограничениями `destination`) |
| `payment-shop` | Платежи во **все** доступные API-магазины |
| `payment-p2p` | Переводы на **любые** счета/телефоны/email |
| `money-source` | Какие способы оплаты приложение поддерживает (см. ниже) |

В одном `scope` нельзя одновременно использовать:

- `payment-p2p` + `payment.to-account`
- `payment-shop` + `payment.to-pattern`

Если строковое значение содержит символы, нарушающие синтаксис `scope`,
применяется JSON-style backslash-escape: `\" \\`.

#### Ограничения для `payment` (формат `имя_права.destination.limit`)

**`destination` (получатель):**

- `to-pattern("<patternId>")` — ограничить конкретным магазином
- `to-account("<id>")` — ограничить счётом (номер кошелька, привязанный
  телефон в формате E.164 без `+`, или email)

Примеры:

```
.to-account("41001XXXXXXXX")
.to-account("79219990099")
.to-account("username@example.ru")
```

**`limit(duration,sum)`:**

- `limit(1,100.50)` — 100,50 ₽ в сутки, юзер может менять сумму
- `limit(,1000)` — одноразовый платёж на ровно 1000 ₽, юзер сумму не меняет
- По умолчанию — `limit(1,3000)` (3000 ₽/сутки, юзер может менять)
- Нельзя в одном `scope` смешивать «за период» и «одноразовые»
- Если `payment` одноразовый — рядом можно только `money-source` и
  `account-info`, остальные права запрещены

#### Право `money-source`

`money-source(...)` — список разрешённых способов оплаты.

- `wallet` — со счёта ЮMoney (по умолчанию)
- `card` — с привязанной к счёту банковской карты юзера. **Недоступно для
  P2P-переводов** (только для платежей в магазины).

Примеры:

```
money-source("wallet","card")
money-source("card")
```

#### Примеры цельных `scope`

```text
# Просмотр баланса и истории
account-info operation-history operation-details

# Платежи в магазин 123, не более 1000 ₽ в неделю + баланс
account-info payment.to-pattern("123").limit(7,1000)

# Переводы на кошелёк XXXX, не более 500 ₽ в 2 недели
payment.to-account("XXXX").limit(14,500)

# Одноразовый перевод по привязанному телефону на 500 ₽
payment.to-account("ZZZ","phone").limit(,500)

# Платежи в магазин 123 с привязанной карты или со счёта (1000 ₽/нед)
payment.to-pattern("123").limit(7,1000) money-source("wallet","card")
```

### 1.7 Формат запроса (общий)

```http
POST /api/<имя_метода> HTTP/1.1
Host: yoomoney.ru
Content-Type: application/x-www-form-urlencoded
Authorization: Bearer <access_token>

param1=value1&param2=value2
```

Все параметры — `application/x-www-form-urlencoded`, кодировка UTF-8.

### 1.8 Формат ответа (общий)

JSON в UTF-8. В ответах платёжных методов — заголовки `Cache-Control: no-cache`
и `Expires` в прошлом.

Ошибки авторизации (заголовок `WWW-Authenticate: Bearer error="…"`):

| HTTP | `error` | Когда |
|---|---|---|
| 400 | `invalid_request` | Запрос не парсится / нет `Authorization` |
| 401 | `invalid_token` | Токен не существует / просрочен / отозван |
| 403 | `insufficient_scope` | У токена нет нужных прав |

`500 Internal Server Error` → ретраить с теми же параметрами.

В ответах могут быть **дополнительные** поля, не описанные в протоколе —
их следует игнорировать.

### 1.9 Типы данных

| Тип протокола | JSON | Описание |
|---|---|---|
| `string` | string | UTF-8 |
| `amount` | number | Дробное, **2 знака после точки** |
| `boolean` | boolean | `true` / `false` |
| `int` | number | int32 |
| `long` | number | int64 |
| `object` | object | Вложенный JSON |
| `array` | array | Массив |
| `datetime` | string | RFC3339, формат `YYYY-MM-DDThh:mm:ss[.f]ZZZZZ`, `Z` или `±hh:mm` обязателен |

Пример datetime: `2019-07-01T19:00:00.000+03:00`.

### 1.10 Уведомление о входящем переводе (notification-p2p-incoming)

Включается на странице https://yoomoney.ru/transfer/myservices/http-notification.

Приходит на `notification_uri`, заданный в настройках, при двух событиях:

- перевод от другого юзера ЮMoney;
- пополнение с банковской карты через виджет «Сбор денег».

**Формат:** `POST application/x-www-form-urlencoded`, UTF-8.
**Ретраи:** 3 попытки — сразу, через 10 минут, через 1 час.

#### Параметры тела

| Поле | Тип | Описание |
|---|---|---|
| `notification_type` | string | `p2p-incoming` (от юзера ЮMoney) или `card-incoming` (с произвольной карты через виджет) |
| `operation_id` | string | ID операции в истории получателя |
| `amount` | amount | Сумма, **поступившая на счёт получателя** (за вычетом комиссии ЮMoney) |
| `withdraw_amount` | amount | Сумма, **списанная** со счёта отправителя |
| `currency` | string | Всегда `643` (RUB, ISO 4217) |
| `datetime` | datetime | Время операции |
| `sender` | string | Номер счёта отправителя; для `card-incoming` — пустая строка |
| `codepro` | boolean | Всегда `false` (переводы с кодом протекции отменены) |
| `label` | string | Метка платежа из `request-payment.label`. Пустая строка, если метки не было |
| `test_notification` | boolean | `true` если это тест с веб-формы. По умолчанию параметра нет |
| `unaccepted` | boolean | Всегда `false` (захолдированные переводы отменены) |
| `sign` | string | **Новая подпись HMAC-SHA256.** Используйте её для проверки |
| `sha1_hash` | string | **DEPRECATED, перестанет приходить с 18 мая 2026.** Не использовать для нового кода |

**Только при HTTPS** (на HTTP — пустые строки):

| Поле | Назначение |
|---|---|
| `lastname`, `firstname`, `fathersname` | ФИО отправителя |
| `email` | Email отправителя |
| `phone` | Телефон отправителя |
| `city`, `street`, `building`, `suite`, `flat`, `zip` | Адрес доставки |

#### Проверка подписи `sign` (актуальный алгоритм, после 18 мая 2026)

`sign` = HMAC-SHA256(secret_key, signing_string) в HEX (lowercase).

`signing_string` строится так:

1. Берём все параметры тела webhook **кроме** самого `sign`.
2. Сортируем по имени по алфавиту (A-Z).
3. Каждое значение URL-кодируем (UTF-8, RFC 3986).
4. Соединяем как `key=value`, разделяя `&`. Пустые значения оставляем как
   `key=` без пробелов.

Пример строки (фрагмент): `amount=0.99&building=12&city=%7B%25%20translate%20%25%7D%D0%9C%D0%BE%D1%81%D0%BA%D0%B2%D0%B0%7B%25%20%2Ftranslate%20%25%7D&codepro=false&...`.

`secret_key` берётся из настроек HTTP-уведомлений на странице
https://yoomoney.ru/transfer/myservices/http-notification (`Секрет`).

**Сравнивать строки в constant time** (`subtle.ConstantTimeCompare` в Go).

#### Проверка `sha1_hash` (старый алгоритм, до 18 мая 2026)

```
sha1_hash = sha1(
  notification_type & operation_id & amount & currency & datetime &
  sender & codepro & NOTIFICATION_SECRET & label
)
```

Где `&` — литеральный амперсанд между значениями. Этот метод устарел, новый
код писать сразу под `sign`.

#### Ответ

`HTTP 200 OK` (любой контент). Любой не-200 → ЮMoney считает доставку
неуспешной и пойдёт в ретрай.

#### Получение остальных данных платежа

Webhook содержит только часть параметров. Чтобы получить остальное (например
`comment`), вызовите `/api/operation-details` с `operation_id` из webhook.

---

## 2. Информация о счёте пользователя

### 2.1 `account-info`

`POST /api/account-info`. Тело пустое. Требует `account-info`.

Ответ:

| Поле | Тип | Описание |
|---|---|---|
| `account` | string | Номер счёта `41001…` |
| `balance` | amount | Баланс |
| `currency` | string | Всегда `643` |
| `account_status` | string | `anonymous` / `named` / `identified` |
| `account_type` | string | `personal` / `professional` |
| `balance_details` | object | Появляется, если когда-либо были задолженности/блокировки/очереди |
| `cards_linked` | array | Привязанные карты (если есть) |

`balance_details`:

| Поле | Описание |
|---|---|
| `total` | Полный баланс |
| `available` | Доступно к расходованию |
| `deposition_pending` | Стоят в очереди |
| `blocked` | Заблокированы исполнительными органами |
| `debt` | Задолженность |
| `hold` | Заморожены |

`cards_linked[i]`:

| Поле | Описание |
|---|---|
| `pan_fragment` | Маскированный номер `510000******9999` |
| `type` | `VISA` / `MasterCard` / `AmericanExpress` / `JCB` (может отсутствовать) |

Пример:

```json
{
  "account": "4100123456789",
  "balance": 1000.00,
  "currency": "643",
  "account_status": "anonymous",
  "account_type": "personal",
  "cards_linked": [
    { "pan_fragment": "510000******9999", "type": "MasterCard" }
  ]
}
```

### 2.2 `operation-history`

`POST /api/operation-history`. Требует `operation-history`. Записи отдаются
в обратном хронологическом порядке.

| Параметр | Тип | Описание |
|---|---|---|
| `type` | string | `deposition` (приход), `payment` (расход), список через пробел. Нет → все |
| `label` | string | Фильтр по `label` запросов `request-payment` |
| `from` | datetime | Включительно |
| `till` | datetime | Не включительно |
| `start_record` | string | Сдвиг (с `0`). Для пагинации — берётся из `next_record` ответа |
| `records` | int | 1..100, по умолчанию 30 |
| `details` | boolean | Если `true`, в каждом элементе будут поля `operation-details`. Требует право `operation-details` |

Ответ:

| Поле | Описание |
|---|---|
| `error` | Код ошибки (только при ошибке) |
| `next_record` | Если есть, в нём номер первой записи следующей страницы |
| `operations[]` | См. ниже |

`operations[i]`:

| Поле | Описание |
|---|---|
| `operation_id` | ID операции |
| `status` | `success` / `refused` / `in_progress` |
| `datetime` | Время |
| `title` | Краткое описание (название магазина или источник пополнения) |
| `pattern_id` | ID шаблона, если это платёж |
| `direction` | `in` / `out` |
| `amount` | Сумма |
| `label` | Метка (для P2P) |
| `type` | `payment-shop` / `outgoing-transfer` / `deposition` / `incoming-transfer` |

При `details=true` дополнительно — все поля `operation-details` (`amount_due`,
`fee`, `sender`, `recipient`, `recipient_type`, `message`, `comment`,
`details`, `digital_goods`).

**Пагинация:** запрашивай первую страницу без `start_record`, в ответе будет
`next_record` если есть ещё страницы. Для следующей страницы повтори запрос с
теми же параметрами + `start_record=<next_record>`.

**Коды ошибок:**

| Код | Описание |
|---|---|
| `illegal_param_type` | Кривой `type` |
| `illegal_param_start_record` | Кривой `start_record` |
| `illegal_param_records` | Кривой `records` |
| `illegal_param_label` | Кривой `label` |
| `illegal_param_from` / `illegal_param_till` | Кривые даты |
| прочее | Техническая, ретраить |

### 2.3 `operation-details`

`POST /api/operation-details`. Требует `operation-details`.

Параметр: `operation_id` (из `operation-history.operation_id` или
`process-payment.payment_id`).

Ответ:

| Поле | Описание |
|---|---|
| `error` | При ошибке |
| `operation_id` | ID |
| `status` | `success` / `refused` / `in_progress` |
| `pattern_id` | Только для платежей |
| `direction` | `in` / `out` |
| `amount` | Сумма списания со счёта |
| `amount_due` | Сумма к получению (только для исходящих P2P) |
| `fee` | Комиссия (только для исходящих P2P) |
| `datetime` | Время |
| `title` | Краткое описание |
| `sender` | Номер счёта отправителя (только для входящих P2P) |
| `recipient` | ID получателя (только для исходящих P2P) |
| `recipient_type` | `account` / `phone` / `email` |
| `message` | Сообщение получателю |
| `comment` | Комментарий (виден отправителю/получателю) |
| `label` | Метка |
| `details` | Произвольный текст с переводами строк |
| `type` | См. `operation-history.type` |
| `digital_goods` | Цифровой товар (пин-коды и т. п.) |

**Коды ошибок:**

| Код | Описание |
|---|---|
| `illegal_param_operation_id` | Не та операция |
| прочее | Техническая |

---

## 3. Платежи из кошелька

### 3.1 Основы (двухфазный платёж)

Оплата делается **в две фазы** на API:

1. `request-payment` — создаёт платёж и проверяет возможность; возвращает
   `request_id`, `contract_amount`, доступные `money_source`.
2. `process-payment` — подтверждает платёж по `request_id`; при необходимости
   запрашивает 3-D Secure.

**Списание происходит на втором шаге** (`process-payment`).

При ретраях `process-payment` с теми же параметрами вернёт состояние **уже
проведённого** платежа (идемпотентно).

При обрыве связи / таймауте — приложение должно повторить вызов с теми же
параметрами.

Виды платежей:

- В магазин (`pattern_id = <scid>`).
- P2P (`pattern_id = p2p`).

### 3.2 `request-payment`

`POST /api/request-payment`. Требует:

- для платежа в магазин — `payment.to-pattern("…")` или `payment-shop`
- для P2P — `payment.to-account("…")` или `payment-p2p`

#### Платёж в магазин

| Параметр | Описание |
|---|---|
| `pattern_id` | `scid` магазина (из ЮKassa) |
| `*` | Параметры шаблона, индивидуальные для магазина |

#### P2P-перевод

| Параметр | Описание |
|---|---|
| `pattern_id` | `p2p` |
| `to` | Номер счёта `41001…` / телефон E.164 без `+` / email |
| `identifier_type` | Опционально — `phone` если в `to` указан телефон |
| `amount` | Сколько **заплатит отправитель** (с комиссией) |
| `amount_due` | Сколько **придёт получателю** (без комиссии). **Указывать или `amount`, или `amount_due`, не оба.** |
| `comment` | Видит отправитель в своей истории |
| `message` | Видит получатель |
| `label` | Метка платежа, до 64 символов, регистрозависимо |

**Комиссия = `contract_amount` − `amount_due`.** Округляется до копеек,
неполные копейки — вверх (т. е. реально может быть до +1 копейки сверху).

#### Ответ

| Поле | Описание |
|---|---|
| `status` | `success` / `refused` |
| `error` | Только при `refused` |
| `request_id` | ID запроса (для `process-payment`). Только при `success` |
| `contract_amount` | Что спишется со счёта (есть и при ошибке `not_enough_funds`) |
| `balance` | Баланс. Появляется, если в токене есть `account-info` |
| `money_source` | Объект с доступными способами оплаты (см. ниже) |
| `recipient_account_status` | Для P2P. `anonymous` / `named` / `identified` |
| `recipient_account_type` | `personal` / `professional` |
| `account_unblock_uri` | При ошибке `account_blocked` |
| `ext_action_uri` | При ошибке `ext_action_required` |

`money_source` имеет до двух полей `wallet` и `cards`:

```json
{
  "wallet": { "allowed": true },
  "cards": {
    "allowed": true,
    "csc_required": true,
    "items": [
      { "id": "card-385244400", "pan_fragment": "5280****7918", "type": "MasterCard" },
      { "id": "card-385244401", "pan_fragment": "4008****7919", "type": "Visa" }
    ]
  }
}
```

`allowed=false` означает «способ доступен в магазине, но юзер не выдавал прав».
Для активации — повторная авторизация с `money-source(...)` в scope.

`request-payment` может выполняться **до 30 секунд** (ходит в магазин),
показывайте юзеру ожидание.

#### Коды ошибок

| Код | Описание |
|---|---|
| `illegal_params` | Нет/кривые обязательные параметры |
| `illegal_param_label` / `illegal_param_to` / `illegal_param_amount` / `illegal_param_amount_due` / `illegal_param_comment` / `illegal_param_message` | Конкретный параметр кривой |
| `not_enough_funds` | Недостаточно средств |
| `payment_refused` | Магазин не принял (например, нет товара) |
| `payee_not_found` | Получатель не найден |
| `authorization_reject` | Транзакция запрещена / не принята оферта |
| `limit_exceeded` | Лимит токена или лимит ЮMoney |
| `account_blocked` | Счёт заблокирован → `account_unblock_uri` |
| `account_closed` | Счёт закрыт |
| `ext_action_required` | Требуется юзеру что-то сделать (идентификация, оферта) → `ext_action_uri` |
| прочее | Техническая, ретраить |

### 3.3 `process-payment`

`POST /api/process-payment`.

| Параметр | Описание |
|---|---|
| `request_id` | Из `request-payment.request_id` |
| `money_source` | `wallet` (по умолчанию) или ID привязанной карты (`card-385244400`) |
| `csc` | CVV2/CVC2 — только при оплате с карты |
| `ext_auth_success_uri` | Куда вернуть юзера после успеха 3-D Secure |
| `ext_auth_fail_uri` | Куда вернуть после фейла 3-D Secure |

#### Ответ

| Поле | Описание |
|---|---|
| `status` | `success` / `refused` / `in_progress` / `ext_auth_required` / прочее |
| `error` | При `refused` |
| `payment_id` | ID платежа (= `operation_id`). Только при `success` |
| `balance` | Баланс после, если в токене есть `account-info` |
| `invoice_id` | Для платежей в магазин |
| `payer` / `payee` | Для P2P — номера счетов |
| `credit_amount` | Сколько ушло получателю (P2P) |
| `account_unblock_uri` | При `account_blocked` |
| `acs_uri`, `acs_params` | При `ext_auth_required` (3-D Secure) |
| `next_retry` | Миллисекунды до следующего ретрая. При `status=in_progress` |
| `digital_goods` | Цифровые товары |

`status=in_progress` — повторять с теми же параметрами через `next_retry`
(или раз в минуту).

#### Сценарий 3-D Secure

1. `request-payment` → есть `cards.allowed=true`
2. `process-payment` с `money_source=card-…&csc=…&ext_auth_success_uri=…&ext_auth_fail_uri=…`
3. Ответ: `status=ext_auth_required`, `acs_uri`, `acs_params`
4. Открываем браузер, делаем `POST acs_uri` с `acs_params` как
   `application/x-www-form-urlencoded`
5. Юзер проходит 3-D Secure, банк-эмитент 302-ит на `ext_auth_success_uri`
   или `ext_auth_fail_uri`
6. Снова `process-payment` с тем же `request_id` (без других параметров)
7. Ответ: `success` или `refused`

#### Коды ошибок

| Код | Описание |
|---|---|
| `contract_not_found` | Нет неподтверждённого платежа с `request_id` |
| `not_enough_funds` | Недостаточно средств |
| `limit_exceeded` | Лимит |
| `money_source_not_available` | Запрошенный способ недоступен |
| `illegal_param_csc` / `illegal_param_ext_auth_success_uri` / `illegal_param_ext_auth_fail_uri` | Кривые параметры |
| `payment_refused` | Магазин отказал / лимит остатка кошелька получателя |
| `authorization_reject` | Карта просрочена / банк отказал / лимит / соглашение |
| `account_blocked` | Счёт заблокирован |
| прочее | Делать **новый платёж** |

#### Цифровые товары

При платеже в магазин цифровых товаров (iTunes, Xbox, игры) в ответе
`process-payment` приходит `digital_goods.article[]` и `digital_goods.bonus[]`
с `serial` и `secret`.

```json
"digital_goods": {
  "article": [
    { "merchantArticleId": "1234567", "serial": "EAV-0087182017", "secret": "87actmdbsv" }
  ],
  "bonus": [
    { "serial": "XXXX-XX-XX", "secret": "0000-1111-2222-3333-4444" }
  ]
}
```

---

## 4. Формы оплаты товаров и услуг (showcase)

API для динамических форм оплаты («квитанций», услуг ЖКХ, телекома и т. п.).
**Авторизации не требует.** В отличие от платёжного API, тут поведение —
как у браузера: всё на HTTP/1.1, заголовки `Location`, `If-Modified-Since`,
коды 300/301/304/404.

Сценарий:

1. Запросить описание формы
2. Показать юзеру, дать заполнить
3. Отправить → проверка
4. Если многошаговая — повторить с описанием следующего шага
5. Если последний шаг → получить параметры для `request-payment`

**Простые формы можно кэшировать на клиенте.** Многошаговые — нельзя.

### 4.1 Поиск реквизитов организаций (`/api/showcase-search`)

```http
GET /api/showcase-search?query=<строка>&records=<N> HTTP/1.1
Host: yoomoney.ru
Accept-Language: ru | en
```

Ответ:

| Поле | Описание |
|---|---|
| `error` | При ошибке |
| `result[]` | Список совпадений |
| `nextPage` | Есть ли ещё |

`result[i]`:

| Поле | Описание |
|---|---|
| `id` | `pattern_id` |
| `title` | Название получателя |
| `url` | Адрес отправки формы |
| `params` | Предзаполненные поля первого шага |
| `format` | `json` (или может отсутствовать) |

### 4.2 Запрос описания формы (`GET /api/showcase/<pattern_id>`)

```http
GET /api/showcase/<pattern_id> HTTP/1.1
Host: yoomoney.ru
Accept-Language: ru | en
Accept-Encoding: gzip
If-Modified-Since: <date>     # если форма уже в кэше клиента
```

| HTTP | Что значит |
|---|---|
| `300 Multiple Choices` | Описание формы. URL для отправки → в `Location` |
| `301 Moved Permanently` | Использовать другую форму, новый URL → в `Location` |
| `304 Not Modified` | Не изменилось со времени `If-Modified-Since` |
| `404 Not Found` | Форма не существует / запрещена |
| `500` | Ретраить |

### 4.3 Отправка формы / шага (`POST <url-из-Location>`)

URL **всегда берётся из `Location`** последнего ответа, никогда не
запоминается между запросами.

```http
POST /api/showcase/validate/<pattern_id>/<step> HTTP/1.1
Host: yoomoney.ru
Content-Type: application/x-www-form-urlencoded

<все видимые UI-контролы>&<все hidden_fields>
```

Невидимые/незаполненные опциональные поля присылаются как `key=` (пустое
значение) — как делают браузеры.

| HTTP | Что значит |
|---|---|
| `200 OK` | Финальный шаг, в теле — параметры для `request-payment` |
| `300` | Есть следующий шаг (описание в теле, URL в `Location`) |
| `400 Bad Request` | Ошибки валидации, текущий шаг с предзаполненными значениями (URL в `Location` для повторной отправки) |
| `404` | Форма не существует / закрыта |
| `500` | Ретраить |

### 4.4 Запрос описания предзаполненной формы (`POST /api/showcase/<pattern_id>`)

То же, что 4.2, но с уже заполненными значениями полей в теле POST.
Используется для повторов ранее совершённых операций. Ответ — как у 4.2 (с
предзаполненными `value`).

### 4.5 Описание формы (form-description JSON)

Корневой документ — JSON в UTF-8 с блоками `title`, `hidden_fields`,
`money_source`, `form`.

```jsonc
{
  "title": "Ростелеком Северо-Запад",
  "hidden_fields": {
    "targetcurrency": "643",
    "ShopArticleID": "35241"
  },
  "money_source": ["wallet", "cards", "payment-card", "cash"],
  "form": [ /* UI-контролы и контейнеры */ ]
}
```

`money_source[]` — допустимые способы оплаты:

| Значение | Что |
|---|---|
| `wallet` | Кошелёк ЮMoney |
| `cards` | Привязанные карты |
| `payment-card` | Произвольная банковская карта |
| `cash` | Наличными |

#### UI-контролы (общие атрибуты)

| Атрибут | Описание |
|---|---|
| `type` | Тип контрола (см. ниже) |
| `name` | Имя поля в POST |
| `value` | Предзаполненное значение |
| `value_autofill` | Макрос автоподстановки на стороне клиента (имеет приоритет над `value`) |
| `hint` | Подсказка для юзера |
| `label` | Подпись над контролом |
| `alert` | Текст ошибки на клиентской валидации |
| `required` | По умолчанию `true` |
| `readonly` | По умолчанию `false` |

#### Типы контролов

| Тип | Что |
|---|---|
| `text` | Текст. Атрибуты: `minlength`, `maxlength`, `pattern` (ECMA-262 RegExp), `keyboard_suggest=number` |
| `number` | Число. Атрибуты: `min`, `max`, `step` |
| `amount` | Сумма (расширение `number`). Атрибуты: `min` (по умолчанию `0.01`), `max`, `step` (по умолчанию `0.01`), `currency` (ISO 4217, по умолчанию `RUB`), `fee` |
| `email` | Email |
| `tel` | Телефон |
| `month` | Месяц |
| `date` | Дата |
| `select` | Выпадающий список |
| `checkbox` | Чекбокс |
| `submit` | Кнопка отправки формы |
| (контейнеры) | `group`, `paragraph` — для группировки и текста |

#### Комиссия покупателя (`fee` блока `amount`)

```
amount = netAmount + fee
fee    = min(max(a * netAmount + b, c), d)
```

| Атрибут | Описание |
|---|---|
| `type` | `std` (стандартная формула) или `custom` (на стороне магазина) |
| `a` | Коэффициент к `netAmount` |
| `b` | Фиксированная сумма |
| `c` | Минимальная комиссия |
| `d` | Максимальная комиссия |
| `amount_type` | `amount` (списать со счёта) или `netAmount` (к получению магазином) |

Комиссия не может быть **меньше 0,01** валюты счёта (округление вверх).

Типовые виды:

| Вид | Коэффициенты |
|---|---|
| Без комиссии | блок `fee` отсутствует |
| % от суммы | `a=<коэф>, b=0, c=0, d=undef` |
| Фиксированная | `a=0, b=<сумма>, c=0, d=undef` |
| % + фикс | `a=<коэф>, b=<сумма>, c=0, d=undef` |
| % ИЛИ минимум | `a=<коэф>, b=0, c=<минимум>, d=undef` |

> Полный референс всех типов контролов и атрибутов (включая `select.options`,
> `checkbox.value`, макросы `value_autofill` и т. д.) — в исходной странице
> https://yoomoney.ru/docs/wallet/showcase/form-description (1100+ строк
> JSON-схемы; для нашего use-case — единого приёма P2P-перевода — детальный
> разбор не нужен, мы не строим динамические формы).

---

## 🔗 Полный индекс источников

| Раздел | URL |
|---|---|
| Главная | https://yoomoney.ru/docs/wallet |
| Авторизация · Общее описание | https://yoomoney.ru/docs/wallet/using-api/authorization/basics |
| Авторизация · Регистрация | https://yoomoney.ru/docs/wallet/using-api/authorization/register-client |
| Авторизация · /oauth/authorize | https://yoomoney.ru/docs/wallet/using-api/authorization/request-access-token |
| Авторизация · /oauth/token | https://yoomoney.ru/docs/wallet/using-api/authorization/obtain-access-token |
| Авторизация · /api/revoke | https://yoomoney.ru/docs/wallet/using-api/authorization/revoke-access-token |
| Авторизация · scope | https://yoomoney.ru/docs/wallet/using-api/authorization/protocol-rights |
| Протокол · Запрос | https://yoomoney.ru/docs/wallet/using-api/format/protocol-request |
| Протокол · Ответ | https://yoomoney.ru/docs/wallet/using-api/format/protocol-response |
| Протокол · Типы | https://yoomoney.ru/docs/wallet/using-api/format/protocol-datatypes |
| Webhook (P2P incoming) | https://yoomoney.ru/docs/wallet/using-api/notification-p2p-incoming |
| account-info | https://yoomoney.ru/docs/wallet/user-account/account-info |
| operation-history | https://yoomoney.ru/docs/wallet/user-account/operation-history |
| operation-details | https://yoomoney.ru/docs/wallet/user-account/operation-details |
| Платежи · Основы | https://yoomoney.ru/docs/wallet/process-payments/basics |
| request-payment | https://yoomoney.ru/docs/wallet/process-payments/request-payment |
| process-payment | https://yoomoney.ru/docs/wallet/process-payments/process-payment |
| Showcase · Основы | https://yoomoney.ru/docs/wallet/showcase/basics |
| Showcase · Поиск | https://yoomoney.ru/docs/wallet/showcase/search |
| Showcase · Описание формы (запрос) | https://yoomoney.ru/docs/wallet/showcase/form-description-request |
| Showcase · Отправка формы | https://yoomoney.ru/docs/wallet/showcase/submit |
| Showcase · Предзаполненная форма | https://yoomoney.ru/docs/wallet/showcase/submit-predefined |
| Showcase · Form description (JSON) | https://yoomoney.ru/docs/wallet/showcase/form-description |

Сопутствующие линки:

- Регистрация приложения: https://yoomoney.ru/myservices/new
- Настройки HTTP-уведомлений: https://yoomoney.ru/transfer/myservices/http-notification
- Условия использования API: https://yoomoney.ru/page?id=526828
- Quickpay (форма оплаты на кошелёк, не часть API): https://yoomoney.ru/docs/payment-buttons
- Форма обратной связи: https://forms.yoomoney.ru/form/api-koshelka-yoomoney
