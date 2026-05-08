# 18. Reply-keyboard в боте: «Подключиться» + «Купить подписку»

**Дата:** 2026-05-08
**Статус:** 🔵 В работе
**Автор:** aziz + Devin
**Связано:** [`research/competitor-extravpn.md`](../research/competitor-extravpn.md) — почему именно эти 2 кнопки

---

## 🎯 Цель

Добавить в Telegram-бот **постоянную reply-keyboard внизу чата** с двумя
кнопками — `🌐 Подключиться` и `🛒 Купить подписку` — и обработчики, которые
показывают карточки статуса прямо в чате (в духе EXTRA VPN, но короче).

Это первый шаг к «бот-кнопочнику»-паттерну: даём юзеру быстрый путь к ключевым
действиям без необходимости открывать Mini App. Mini App остаётся для всех
расширенных операций.

## 📚 Контекст

По проду (см. `vpn-stats.sh` от 2026-05-08):
- 373 юзера зарегистрировано, 5 реальных платящих
- 184 trial-юзера активны, 11 платных подписок
- **Из 338 новых за 7 дней реально юзают VPN только 47** (14% активация)

Узкое место — активация: юзер пришёл, увидел Mini App, не разобрался, ушёл.
Reply-keyboard внизу чата делает action-points **постоянно видимыми** даже у
юзеров, которые никогда не открывали Mini App. Заодно — карточки статуса в
чате (не просто кнопки) дают быстрый ответ «у меня сколько дней осталось».

## 🏗 Архитектура

### UX-flow

```
─── /start ──────────────────────────────
[ Welcome text — БЕЗ ИЗМЕНЕНИЙ ]
[ 🚀 Открыть приложение ]                ← inline (как сейчас)

↓ потом 2-е сообщение:
"👇 Выбери действие:"
⌨️ reply-keyboard (is_persistent=true, resize_keyboard=true):
[ 🌐 Подключиться │ 🛒 Купить подписку ]

─── Tap «🌐 Подключиться» ───────────────
Бот → 1 сообщение в чат:
  📡 Твоя подписка
  📅 X дн. (до DD.MM.YYYY)
  📱 Устройств: N
  📊 Трафик: K Б
  🆔 ID: <telegram_id>
  Inline:
  [ 📱 Подключиться (Mini App /connect) │ 🔑 Получить ссылку ]

  ↳ если подписки нет: «Подписки нет, оформи тариф»
     Inline: [ 💳 К тарифам │ 📱 Открыть приложение ]

  ↳ tap «🔑 Получить ссылку» → callback `get_sub_link`
     → бот edit-ит сообщение, кладёт https://cdn.osmonai.com/sub/<token>
     + кнопку «📱 Подключиться» (прямой URL)

─── Tap «🛒 Купить подписку» ───────────
Бот → 1 сообщение в чат:
  🛒 Тарифы
  • 1 месяц — 199 ₽
  • 3 месяца — 550 ₽   (-8%)
  • 6 месяцев — 1100 ₽ (-15%)
  • 12 месяцев — 1999 ₽ (-30%) ⭐
  Базово 2 устройства, можно расширить в приложении
  Inline:
  [ 💳 1мес — 199₽ │ 📱 Все тарифы ]

  ↳ tap «💳 1мес — 199₽» → callback `buy_quick_<plan_id>`
     → создаём invoice через paymentClient.CreateInvoice(uid, 1, 2, "wata")
     → edit сообщение: «💳 К оплате 199₽ · Счёт активен 30 минут»
        Inline: [ 💳 Оплатить 199₽ (URL=invoice_link) │ ❌ Отмена ]

  ↳ tap «❌ Отмена» → callback `cancel_invoice`
     → edit «❌ Оплата отменена. Чтобы попробовать снова — нажми «🛒 Купить подписку».»

  ↳ tap «📱 Все тарифы» → открывает Mini App /plans
```

### Telegram-ограничение и как его обходим

**Одно сообщение = один `reply_markup`** — нельзя одновременно inline-кнопки
и reply-keyboard. Поэтому:

- **На `/start`** шлём **2 сообщения**:
  1. UNCHANGED welcome с inline `🚀 Открыть приложение`
  2. NEW мини-сообщение `"👇 Выбери действие:"` с reply-keyboard

- **На дальнейших ответах** — одно сообщение с inline (карточка + 2 кнопки).
  Reply-keyboard остаётся видимой за счёт `is_persistent=true` (Bot API 6.5+,
  поддерживается всеми клиентами с янв 2023). НЕ пере-аттачим, чтобы не
  спамить лишними сообщениями.

### Сервис-границы

Не создаём новых сервисов. Всё в gateway, через существующие gRPC-клиенты:
- `subscriptionClient.GetActiveSubscription` — для карточки статуса
- `subscriptionClient.ListPlans` — для списка тарифов
- `vpnClient.GetSubscriptionToken` — для «Получить ссылку»
- `paymentClient.CreateInvoice` — для quick-buy
- `authClient.GetUserByTelegramID` — для маппинга telegram_id → user_id (если требуется)

## 📁 Файлы для изменений

### 1. `vpn_go/platform/pkg/telegram/client.go`

**Добавляем типы:**

```go
// KeyboardButton — кнопка в reply-keyboard. Поддерживаем text и web_app.
// https://core.telegram.org/bots/api#keyboardbutton
type KeyboardButton struct {
    Text   string      `json:"text"`
    WebApp *WebAppInfo `json:"web_app,omitempty"`
}

// ReplyKeyboardMarkup — постоянная клавиатура внизу чата.
// is_persistent=true (Bot API 6.5+) держит её видимой между сообщениями
// без необходимости пере-аттачить. resize_keyboard=true делает кнопки
// компактнее (нативный layout мобильного клиента).
// https://core.telegram.org/bots/api#replykeyboardmarkup
type ReplyKeyboardMarkup struct {
    Keyboard       [][]KeyboardButton `json:"keyboard"`
    IsPersistent   bool               `json:"is_persistent,omitempty"`
    ResizeKeyboard bool               `json:"resize_keyboard,omitempty"`
}
```

**Меняем тип `SendMessageParams.ReplyMarkup`:**

```go
type SendMessageParams struct {
    ChatID                int64  `json:"chat_id"`
    Text                  string `json:"text"`
    ParseMode             string `json:"parse_mode,omitempty"`
    ReplyMarkup           any    `json:"reply_markup,omitempty"` // было *InlineKeyboardMarkup
    DisableWebPagePreview bool   `json:"disable_web_page_preview,omitempty"`
}
```

`any` нужно потому что Telegram API поле `reply_markup` — union (может быть
inline, reply, remove, или force-reply). Существующие 5 callers передают
`*InlineKeyboardMarkup` — JSON-сериализация не меняется (Go marshall'ит
структуру по тегам в обоих случаях).

### 2. `vpn_go/services/gateway/internal/app/app.go`

**Меняем wiring `NewTelegramBotHandler`:**

```go
// было:
botHandler := handler.NewTelegramBotHandler(
    telegramClient, subscriptionClient, authClient,
    broadcastClient, promoClient, logger, channelUsername)

// стало:
botHandler := handler.NewTelegramBotHandler(
    telegramClient, subscriptionClient, authClient,
    broadcastClient, promoClient, paymentClient, vpnClient,
    logger, channelUsername)
```

`paymentClient` и `vpnClient` уже есть в `App`-struct — просто пробрасываем
как параметры.

### 3. `vpn_go/services/gateway/internal/handler/telegram_bot.go`

#### 3.1. Расширение struct и конструктора

```go
type TelegramBotHandler struct {
    telegramClient     *telegram.Client
    subscriptionClient *client.SubscriptionClient
    authClient         *client.AuthClient
    broadcastClient    *client.BroadcastClient
    promoClient        *client.PromoClient
    paymentClient      *client.PaymentClient   // NEW
    vpnClient          *client.VPNClient       // NEW
    logger             *zap.Logger
    channelUsername    string
}
```

#### 3.2. `sendStartMessage` — добавить второе сообщение

После существующего `SendMessage` шлём:

```go
const replyHelpText = "👇 Выбери действие:"
replyKB := &telegram.ReplyKeyboardMarkup{
    Keyboard: [][]telegram.KeyboardButton{
        {
            {Text: replyBtnConnect},
            {Text: replyBtnBuy},
        },
    },
    IsPersistent:   true,
    ResizeKeyboard: true,
}
_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
    ChatID:      chatID,
    Text:        replyHelpText,
    ReplyMarkup: replyKB,
})
```

Текст reply-кнопок выносим в константы:
```go
const (
    replyBtnConnect = "🌐 Подключиться"
    replyBtnBuy     = "🛒 Купить подписку"
)
```

#### 3.3. `handleCommand` — text-match на reply-кнопки

```go
switch {
case cmd == "/start":
    h.handleStart(...)
case text == replyBtnConnect:
    h.handleConnectButton(ctx, msg.Chat.ID, msg.From.ID)
case text == replyBtnBuy:
    h.handleBuyButton(ctx, msg.Chat.ID, msg.From.ID)
// ... rest unchanged
}
```

Важно: text-match **до** проверки prefix-команд. Если юзер отправит
`"🌐 Подключиться"` — это попадёт в новый case. Но если кто-то напишет
"/admin" — попадёт в admin case. Конфликта нет, символы разные.

#### 3.4. `handleConnectButton`

```go
func (h *TelegramBotHandler) handleConnectButton(ctx context.Context, chatID, telegramID int64) {
    sub, err := h.subscriptionClient.GetActiveSubscription(ctx, telegramID)
    if err != nil || sub == nil || !sub.HasActive || sub.Subscription == nil {
        // Нет активной подписки — CTA на оплату
        text := "📡 У тебя пока нет активной подписки.\n\nВыбери тариф — получишь ключ сразу после оплаты."
        kb := &telegram.InlineKeyboardMarkup{
            InlineKeyboard: [][]telegram.InlineKeyboardButton{{
                {Text: "💳 К тарифам", CallbackData: cbBuyPrompt},
                {Text: "📱 Открыть приложение", WebApp: &telegram.WebAppInfo{URL: webAppPlansURL}},
            }},
        }
        _ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
            ChatID: chatID, Text: text, ReplyMarkup: kb, ParseMode: "HTML",
        })
        return
    }

    // Активная подписка — карточка статуса
    s := sub.Subscription
    expires, _ := time.Parse(time.RFC3339, s.ExpiresAt)
    daysLeft := int(time.Until(expires).Hours() / 24)
    if daysLeft < 0 { daysLeft = 0 }

    text := fmt.Sprintf(
        "📡 <b>Твоя подписка</b>\n"+
        "─────────\n"+
        "📅 Осталось: <b>%d дн.</b> (до %s)\n"+
        "📱 Устройств: <b>%d</b>\n"+
        "🆔 ID: <code>%d</code>",
        daysLeft, expires.Format("02.01.2006"),
        s.MaxDevices,
        telegramID,
    )
    kb := &telegram.InlineKeyboardMarkup{
        InlineKeyboard: [][]telegram.InlineKeyboardButton{{
            {Text: "📱 Подключиться", WebApp: &telegram.WebAppInfo{URL: webAppConnectURL}},
            {Text: "🔑 Получить ссылку", CallbackData: cbGetSubLink},
        }},
    }
    _ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
        ChatID: chatID, Text: text, ReplyMarkup: kb, ParseMode: "HTML",
    })
}
```

#### 3.5. `handleBuyButton`

```go
func (h *TelegramBotHandler) handleBuyButton(ctx context.Context, chatID, telegramID int64) {
    plansResp, err := h.subscriptionClient.ListPlans(ctx, true)
    if err != nil || plansResp == nil || len(plansResp.Plans) == 0 {
        h.logger.Error("ListPlans failed in handleBuyButton", zap.Error(err))
        _ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
            ChatID: chatID,
            Text:   "❌ Не удалось загрузить тарифы. Попробуй позже или открой приложение.",
        })
        return
    }

    // Сортируем по duration_days ASC, фильтруем "видимые" (без promo plan_id=101)
    plans := filterVisiblePlans(plansResp.Plans)

    // Текст: список тарифов с скидкой относительно 1мес
    var sb strings.Builder
    sb.WriteString("🛒 <b>Тарифы</b>\n─────────\n")
    base := basePricePerMonth(plans) // цена 1мес
    var quickBuyPlanID int32
    var quickBuyPrice string
    for _, p := range plans {
        months := p.DurationDays / 30
        priceN, _ := strconv.ParseFloat(p.BasePrice, 64)
        discount := ""
        if months > 1 && base > 0 {
            full := base * float64(months)
            if priceN < full {
                pct := int(math.Round((1 - priceN/full) * 100))
                discount = fmt.Sprintf(" (-%d%%)", pct)
            }
        }
        star := ""
        if months == 12 {
            star = " ⭐"
        }
        sb.WriteString(fmt.Sprintf("• %s — <b>%s ₽</b>%s%s\n",
            humanDuration(p.DurationDays), formatRub(p.BasePrice), discount, star))
        if months == 1 {
            quickBuyPlanID = p.Id
            quickBuyPrice = formatRub(p.BasePrice)
        }
    }
    sb.WriteString("\n<i>Базово 2 устройства · можно расширить в приложении</i>")

    // 2 inline кнопки
    quickBuyText := fmt.Sprintf("💳 1мес — %s₽", quickBuyPrice)
    kb := &telegram.InlineKeyboardMarkup{
        InlineKeyboard: [][]telegram.InlineKeyboardButton{{
            {Text: quickBuyText, CallbackData: fmt.Sprintf("%s%d", cbBuyQuickPrefix, quickBuyPlanID)},
            {Text: "📱 Все тарифы", WebApp: &telegram.WebAppInfo{URL: webAppPlansURL}},
        }},
    }
    _ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
        ChatID: chatID, Text: sb.String(), ReplyMarkup: kb, ParseMode: "HTML",
    })
}
```

#### 3.6. Callbacks

```go
const (
    cbGetSubLink     = "get_sub_link"
    cbBuyPrompt      = "buy_prompt"
    cbBuyQuickPrefix = "buy_quick_"
    cbCancelInvoice  = "cancel_invoice"
)

// В handleCallback:
switch {
case callback.Data == cbGetSubLink:
    h.handleGetSubLink(ctx, callback)
case callback.Data == cbBuyPrompt:
    h.handleBuyButton(ctx, callback.Message.Chat.ID, callback.From.ID)
    h.answerCallback(ctx, callback.ID, "", false)
case strings.HasPrefix(callback.Data, cbBuyQuickPrefix):
    planIDStr := strings.TrimPrefix(callback.Data, cbBuyQuickPrefix)
    h.handleBuyQuick(ctx, callback, planIDStr)
case callback.Data == cbCancelInvoice:
    h.handleCancelInvoice(ctx, callback)
// ... existing claim_bonus / bc_* cases unchanged
}
```

**`handleGetSubLink`:**
- Дёргает `vpnClient.GetSubscriptionToken(telegramID)`
- Edit-ит исходное сообщение: добавляет URL + кнопку «📱 Подключиться» (URL=sub-link)
- `answerCallback("Ссылка получена!", false)`

**`handleBuyQuick`:**
- Парсит `planID int32`
- Дёргает `paymentClient.CreateInvoice(telegramID, planID, 2, "wata")`
- Edit-ит сообщение: «💳 К оплате X₽ · Счёт активен 30 минут» + кнопки
  `[💳 Оплатить X₽ (URL=invoice_link) │ ❌ Отмена]`
- `answerCallback("Счёт создан", false)`

**`handleCancelInvoice`:**
- Edit-ит сообщение: «❌ Оплата отменена. Нажми «🛒 Купить подписку» чтобы начать заново.»
- Без кнопок (или с одной — «🛒 Купить подписку» как callback `buy_prompt`)
- `answerCallback("Отменено", false)`

### 4. URL-константы

В верхушке файла:

```go
const (
    webAppRootURL    = "https://cdn.osmonai.com"
    webAppPlansURL   = "https://cdn.osmonai.com/plans"
    webAppConnectURL = "https://cdn.osmonai.com/connect"
)
```

Существующий `sendStartMessage` использует hardcoded `https://cdn.osmonai.com`
— заменим на `webAppRootURL`.

## 🧪 Тесты

Новый файл `telegram_bot_buttons_test.go`:

```go
TestSendStartMessage_AddsReplyKeyboard
  // После /start отправлено 2 сообщения, второе имеет ReplyKeyboardMarkup
  // с двумя кнопками "🌐 Подключиться" и "🛒 Купить подписку".

TestHandleConnectButton_NoSubscription
  // GetActiveSubscription возвращает HasActive=false →
  // отправлено сообщение с CTA + 2 inline кнопками.

TestHandleConnectButton_WithSubscription
  // GetActiveSubscription возвращает active sub →
  // отправлена карточка с днями + 2 inline.

TestHandleBuyButton_RendersPlans
  // ListPlans возвращает 4 плана →
  // текст содержит все 4 + правильные скидки + ⭐ на 12мес.
  // 2 inline: quick-buy на самый дешёвый + Mini App.

TestHandleBuyQuick_CreatesInvoice
  // Callback buy_quick_1 → CreateInvoice вызван →
  // EditMessage с invoice_link в URL-кнопке.

TestHandleCancelInvoice
  // Callback cancel_invoice → EditMessage с текстом отмены.
```

Mock'и для `subscriptionClient`, `paymentClient`, `vpnClient`, `telegramClient`
— interface'ы в helper-файле тестов.

## ✅ Acceptance criteria

1. После `/start` юзер видит 2 сообщения: welcome (как был) + «👇 Выбери действие:» с reply-keyboard `[🌐 Подключиться │ 🛒 Купить подписку]`.
2. Reply-keyboard остаётся видимой между сообщениями (`is_persistent=true`).
3. Tap «Подключиться» с активной подпиской → карточка статуса + 2 inline.
4. Tap «Подключиться» без подписки → CTA + 2 inline.
5. Tap «🔑 Получить ссылку» → URL подписки выводится в edit-нутом сообщении.
6. Tap «Купить подписку» → список тарифов с скидками + 2 inline.
7. Tap «💳 1мес — 199₽» → invoice создан, кнопка «Оплатить» с URL.
8. Tap «❌ Отмена» → сообщение редактируется.
9. Tap «📱 Все тарифы» / «📱 Подключиться» → открывается Mini App на нужной странице.
10. Существующие команды (`/admin`, `/promo`, `/bonus`, deep-link `/start ref_*`/`src_*`) работают как раньше.

## 🚫 Что НЕ делаем

- Внутренний баланс / «Пополнить баланс» (отложено).
- Reply-кнопки «Рефералы» / «Информация» (Mini App информативнее).
- Channel-subscription gate (режет конверсию).
- Crypto-платежи, gift, auto-renew, rotate-token, user-promo-input.
- Изменения в Mini App (`vpn_next/`).
- Per-device-pricing UI в чате (списком 2-99 как у конкурента).

## 📊 Ожидаемые метрики

- **Tap-rate на reply-кнопки** — сколько % юзеров используют их за сутки.
- **Конверсия `🌐 Подключиться → Connect/Sub-link`** — особенно для trial-юзеров.
- **Конверсия `🛒 Купить → invoice → paid`** через quick-buy vs Mini App.

Метрики снимаем через `bot_starts` + новые поля или через парсинг логов
gateway. Отдельная задача — вешать аналитику на новые callback'и.

## 🔗 Файлы

- Source: `vpn_go/services/gateway/internal/handler/telegram_bot.go`
- Source: `vpn_go/platform/pkg/telegram/client.go`
- Source: `vpn_go/services/gateway/internal/app/app.go`
- Tests: `vpn_go/services/gateway/internal/handler/telegram_bot_buttons_test.go` (NEW)
- Research: `vpn_go/docs/research/competitor-extravpn.md`
