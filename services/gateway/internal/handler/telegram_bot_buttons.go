// Package handler — telegram_bot_buttons.go: reply-keyboard внизу чата
// (`🌐 Подключиться` / `🛒 Купить подписку`) + обработчики этих кнопок и
// связанных inline-callback'ов (см. docs/tasks/18-bot-reply-keyboard.md).
//
// Отдельный файл, чтобы не раздувать telegram_bot.go. В telegram_bot.go
// остались только точки расширения: text-match в handleCommand, диспатч
// callback'ов, и второе сообщение от sendStartMessage.
//
// UX-flow целиком (карточка статуса, quick-buy, отмена счёта) описан в
// 18-bot-reply-keyboard.md. Здесь — реализация без отступлений от него,
// КРОМЕ ОДНОГО: задача в MD местами говорит «передавать telegram_id в
// subscription/payment-сервисы», но эти сервисы оперируют внутренним
// users.id (см. фикс /bonus от того же дня — task 18 был написан до его
// обнаружения). Здесь честно резолвим telegram_id → users.id через
// authClient.GetUserByTelegramID, иначе у юзеров с users.id != telegram_id
// (т.е. почти у всех) handler'ы возвращали бы пустые ответы / NotFound.
package handler

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vpn/platform/pkg/telegram"
	subpb "github.com/vpn/shared/pkg/proto/subscription/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─── Константы UI ─────────────────────────────────────────────────

// Тексты reply-кнопок. Используются И на отрисовке клавы в sendStartMessage,
// И при text-match в handleCommand — поэтому в одном месте.
const (
	replyBtnConnect = "🌐 Подключиться"
	replyBtnBuy     = "🛒 Купить подписку"
	replyHelpText   = "👇 Выбери действие:"
)

// URL-ы Mini App. Хардкод prod-домена — раньше так было в sendStartMessage,
// просто вынесли в константы. При появлении staging — параметризовать через
// config (Telegram.WebhookURL у Gateway).
const (
	webAppRootURL    = "https://cdn.osmonai.com"
	webAppPlansURL   = "https://cdn.osmonai.com/plans"
	webAppConnectURL = "https://cdn.osmonai.com/connect"
)

// Callback-data префиксы для inline-кнопок, которые рисуют handleConnectButton/
// handleBuyButton. Длина < 64 байт (лимит Telegram).
//
// cbConnectPrompt / cbBuyPrompt продублированы в welcome-сообщении (см.
// sendStartMessage), чтобы юзер мог открыть статус/тарифы прямо из welcome
// без необходимости тапать reply-кнопку внизу. Оба callback'а ведут на те
// же хендлеры, что и text-match'и reply-кнопок.
const (
	cbGetSubLink     = "get_sub_link"
	cbConnectPrompt  = "connect_prompt"
	cbBuyPrompt      = "buy_prompt"
	cbBuyQuickPrefix = "buy_quick_"
	cbCancelInvoice  = "cancel_invoice"
)

// promoPlanIDExcluded — id плана «Промо 79₽», который выдаётся через
// /promo @user'ом и НЕ должен светиться в общем списке тарифов /buy.
// 101 захардкожен и в telegram_bot_promo.go (promoPlanID); если поменяется
// — синхронизировать оба места.
const promoPlanIDExcluded int32 = 101

// Ссылки на сторы Happ — берутся из Mini App `/connect`. iOS/macOS используют
// один и тот же listing "Happ Proxy Utility Plus" (id=6746188973) — это RU-сторе
// версия без NetworkExtension-ограничений. Если меняешь URL'ы — синхронизируй
// с vpn_next/app/connect/page.tsx (HAPP_APPSTORE_RU/MACOS_APPSTORE_RU и др.).
const (
	happStoreAndroid = "https://play.google.com/store/apps/details?id=com.happproxy"
	happStoreIOS     = "https://apps.apple.com/ru/app/happ-proxy-utility-plus/id6746188973"
	happStoreMacOS   = "https://apps.apple.com/ru/app/happ-proxy-utility-plus/id6746188973"
	happStoreWindows = "https://github.com/Happ-proxy/happ-desktop/releases/latest"
)

// botBuyDefaultProvider — провайдер для quick-buy из бота. wata = рублёвая
// форма, доступна без Mini App (юзер кликает прямо в TG → открывается URL).
// Для Stars пришлось бы открывать Mini App.openInvoice(), что в чисто
// бот-flow не работает.
const botBuyDefaultProvider = "wata"

// botBuyDefaultMaxDevices — для quick-buy фиксируем 2 устройства (как в
// карточке Mini App.plans базовый вариант). Изменение per-device count —
// через Mini App, где есть слайдер.
const botBuyDefaultMaxDevices int32 = 2

// botInvoiceTTLMinutes — wata invoice живёт примерно столько (см. конфиг
// payment-service.WataConfig). Для текста сообщения, не валидируем здесь.
const botInvoiceTTLMinutes = 30

// ─── Reply-keyboard ───────────────────────────────────────────────

// newReplyKeyboardMarkup — общий конструктор постоянной reply-клавиатуры
// внизу чата (🌐 Подключиться | 🛒 Купить подписку). is_persistent=true
// держит её видимой между ответами бота без необходимости пере-аттачить,
// resize_keyboard=true — компактный layout мобильного клиента.
func newReplyKeyboardMarkup() *telegram.ReplyKeyboardMarkup {
	return &telegram.ReplyKeyboardMarkup{
		Keyboard: [][]telegram.KeyboardButton{
			{
				{Text: replyBtnConnect},
				{Text: replyBtnBuy},
			},
		},
		IsPersistent:   true,
		ResizeKeyboard: true,
	}
}

// sendReplyKeyboard отправляет 2-е сообщение после /start с постоянной
// клавиатурой внизу. Telegram не разрешает в одном сообщении одновременно
// inline (в welcome) и reply-клаву, поэтому шлём отдельным сообщением.
//
// Сейчас не используется напрямую: reply-клавиатура прикрепляется ко 2-му
// сообщению /start (sendPostStartLinkOrCTA), которое уже несёт полезный
// контент. Оставлено как fallback на случай если потребуется отправить
// reply-клаву отдельно (без content).
func (h *TelegramBotHandler) sendReplyKeyboard(ctx context.Context, chatID int64) {
	if err := h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:      chatID,
		Text:        replyHelpText,
		ReplyMarkup: newReplyKeyboardMarkup(),
	}); err != nil {
		h.logger.Warn("failed to send reply keyboard",
			zap.Int64("chat_id", chatID), zap.Error(err))
	}
}

// sendPostStartLinkOrCTA отправляет 2-е сообщение после /start. Вместе с
// ним прикрепляется reply-клавиатура внизу чата (🌐 Подключиться / 🛒 Купить
// подписку), чтобы у юзера сразу появились постоянные кнопки.
//
// Контент зависит от состояния подписки:
//   - Активная подписка (trial / paid) — VLESS-ссылка с инструкцией по
//     подключению через Happ. Текст идентичен handleGetSubLink, чтобы юзер
//     при /start сразу видел готовый ключ.
//   - Подписки нет / истекла / любая ошибка по пути — CTA "купи подписку"
//     с подсказкой нажать reply-кнопку «🛒 Купить подписку» внизу.
//
// Telegram запрещает inline-кнопки в одном сообщении с reply-клавиатурой,
// поэтому здесь только текст: ссылка копируется тапом по <code>, тарифы и
// сторы Happ юзер открывает через welcome-inline (1-е сообщение) либо
// reply-кнопки внизу.
func (h *TelegramBotHandler) sendPostStartLinkOrCTA(ctx context.Context, chatID, userID, telegramID int64) {
	text := h.buildPostStartText(ctx, userID, telegramID)
	if err := h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   "HTML",
		ReplyMarkup: newReplyKeyboardMarkup(),
	}); err != nil {
		h.logger.Warn("failed to send post-start info",
			zap.Int64("chat_id", chatID),
			zap.Int64("tg_id", telegramID),
			zap.Error(err))
	}
}

// buildPostStartText формирует текст 2-го сообщения /start.
// Возвращает либо инструкцию с VLESS-ссылкой, либо CTA на покупку.
// Любая ошибка по пути (sub/vpn-сервис недоступен, нет токена, нет активной
// подписки) → CTA. Это безопаснее чем падать без сообщения вовсе.
func (h *TelegramBotHandler) buildPostStartText(ctx context.Context, userID, telegramID int64) string {
	const ctaText = "🛒 <b>У тебя пока нет активной подписки.</b>\n\n" +
		"Чтобы подключиться к VPN — оформи подписку.\n" +
		"Нажми кнопку <b>«🛒 Купить подписку»</b> внизу — выбери тариф и оплати, " +
		"ключ придёт сразу после оплаты."

	if h.subscriptionClient == nil || h.vpnClient == nil || userID <= 0 {
		return ctaText
	}

	subResp, err := h.subscriptionClient.GetActiveSubscription(ctx, userID)
	if err != nil {
		h.logger.Warn("post-start: GetActiveSubscription failed",
			zap.Int64("tg_id", telegramID), zap.Int64("user_id", userID), zap.Error(err))
		return ctaText
	}
	if subResp == nil || !subResp.HasActive {
		return ctaText
	}

	tok, err := h.vpnClient.GetSubscriptionToken(ctx, userID)
	if err != nil || tok == nil || tok.SubscriptionToken == "" {
		h.logger.Warn("post-start: GetSubscriptionToken failed",
			zap.Int64("tg_id", telegramID), zap.Int64("user_id", userID), zap.Error(err))
		return ctaText
	}

	// Тот же baseURL-fallback, что и в handleGetSubLink: env PUBLIC_BASE_URL
	// либо webAppRootURL (prod-домен Mini App).
	baseURL := os.Getenv("PUBLIC_BASE_URL")
	if baseURL == "" {
		baseURL = webAppRootURL
	}
	subURL := fmt.Sprintf("%s/api/v1/subscription/%s", baseURL, tok.SubscriptionToken)

	return fmt.Sprintf(
		"🔑 <b>Твой VPN-ключ</b>\n"+
			"─────────\n"+
			"<code>%s</code>\n"+
			"<i>Тапни по ссылке выше, чтобы скопировать.</i>\n\n"+
			"📥 <b>Подключись через Happ/INCY:</b>\n"+
			"1. Скачай приложение для своей платформы — кнопки в Mini App "+
			"(нажми «🚀 Открыть приложение» сверху → раздел «Подключение»).\n"+
			"2. В приложении нажми <b>«+»</b> → вставь скопированную ссылку → <b>«Добавить»</b>.\n"+
			"3. Нажми кнопку включения → выбери сервер.\n\n"+
			"💡 Ссылка обновляется автоматически — серверы будут добавляться сами.",
		subURL,
	)
}

// ─── User resolution (telegram_id → users.id) ─────────────────────

// resolveUserIDForBot мапит telegram_id (из callback.From.ID / msg.From.ID)
// во внутренний users.id через AuthService.GetUserByTelegramID. Возвращает
// (userID, registered, err):
//   - registered=false → юзер ещё не открыл Mini App (auth-service вернул
//     NotFound). Это не ошибка — bot-handler сам решает, что показать
//     («открой приложение, чтобы активировать аккаунт»).
//   - err != nil → реальный сбой gRPC. Логируем + показываем юзеру generic.
func (h *TelegramBotHandler) resolveUserIDForBot(ctx context.Context, telegramID int64) (int64, bool, error) {
	resp, err := h.authClient.GetUserByTelegramID(ctx, telegramID)
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return 0, false, nil
		}
		return 0, false, err
	}
	if resp == nil || resp.User == nil {
		return 0, false, nil
	}
	return resp.User.Id, true, nil
}

// ─── /🌐 Подключиться ─────────────────────────────────────────────

// handleConnectButton — text-match на "🌐 Подключиться" (reply-кнопка) либо
// callback `cbBuyPrompt` (после "Нет подписки → купить").
//
// Логика:
//   - Юзер ещё не открыл Mini App → "Открой приложение, чтобы активировать"
//   - Активной подписки нет        → CTA на тарифы (callback buy_prompt + Mini App)
//   - Есть активная подписка       → карточка статуса + Mini App.connect + get_sub_link
func (h *TelegramBotHandler) handleConnectButton(ctx context.Context, chatID, telegramID int64) {
	userID, registered, err := h.resolveUserIDForBot(ctx, telegramID)
	if err != nil {
		h.logger.Error("connect button: failed to resolve user",
			zap.Int64("tg_id", telegramID), zap.Error(err))
		_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Не удалось получить статус. Попробуй ещё раз через минуту.",
		})
		return
	}
	if !registered {
		h.sendOpenAppPrompt(ctx, chatID,
			"📡 Чтобы посмотреть статус подключения, сначала открой приложение — там активируется твой аккаунт.")
		return
	}

	subResp, err := h.subscriptionClient.GetActiveSubscription(ctx, userID)
	if err != nil {
		h.logger.Error("connect button: GetActiveSubscription failed",
			zap.Int64("tg_id", telegramID), zap.Int64("user_id", userID), zap.Error(err))
		_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Не удалось получить статус. Попробуй ещё раз через минуту.",
		})
		return
	}

	// Нет активной подписки → CTA на тарифы.
	if subResp == nil || !subResp.HasActive || subResp.Subscription == nil {
		text := "📡 У тебя пока нет активной подписки.\n\nВыбери тариф — получишь ключ сразу после оплаты."
		kb := &telegram.InlineKeyboardMarkup{
			InlineKeyboard: [][]telegram.InlineKeyboardButton{{
				{Text: "💳 К тарифам", CallbackData: cbBuyPrompt},
				{Text: "📱 Открыть приложение", WebApp: &telegram.WebAppInfo{URL: webAppPlansURL}},
			}},
		}
		_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
			ChatID:      chatID,
			Text:        text,
			ParseMode:   "HTML",
			ReplyMarkup: kb,
		})
		return
	}

	// Активная подписка → карточка статуса.
	text := formatSubscriptionCard(subResp.Subscription, telegramID)
	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{{
			{Text: "📱 Подключиться", WebApp: &telegram.WebAppInfo{URL: webAppConnectURL}},
			{Text: "🔑 Получить ссылку", CallbackData: cbGetSubLink},
		}},
	}
	_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   "HTML",
		ReplyMarkup: kb,
	})
}

// formatSubscriptionCard — текст карточки статуса для handleConnectButton.
// daysLeft считаем из expires_at, отрицательные значения клемим в 0
// (хотя для status='active' такого быть не должно).
func formatSubscriptionCard(s *subpb.Subscription, telegramID int64) string {
	expires, _ := time.Parse(time.RFC3339, s.ExpiresAt)
	daysLeft := int(time.Until(expires).Hours() / 24)
	if daysLeft < 0 {
		daysLeft = 0
	}
	statusEmoji := "✅"
	if s.Status == "trial" {
		statusEmoji = "🆓"
	}
	return fmt.Sprintf(
		"📡 <b>Твоя подписка</b>\n"+
			"─────────\n"+
			"%s Статус: <b>%s</b>\n"+
			"📅 Осталось: <b>%d дн.</b> (до %s)\n"+
			"📱 Устройств: <b>%d</b>\n"+
			"🆔 ID: <code>%d</code>",
		statusEmoji, s.Status,
		daysLeft, expires.Format("02.01.2006"),
		s.MaxDevices,
		telegramID,
	)
}

// handleGetSubLink — callback "🔑 Получить ссылку". Дёргает
// vpnClient.GetSubscriptionToken (тот выдаёт уже готовый sub-URL'у), и
// edit-ит исходное сообщение, добавляя URL текстом + кнопку «Подключиться»
// с прямым URL (Telegram-клиенты подсветят как ссылку).
func (h *TelegramBotHandler) handleGetSubLink(ctx context.Context, callback *CallbackQuery) {
	if h.vpnClient == nil || callback.Message == nil {
		h.answerCallback(ctx, callback.ID, "❌ Недоступно", true)
		return
	}
	telegramID := callback.From.ID
	userID, registered, err := h.resolveUserIDForBot(ctx, telegramID)
	if err != nil || !registered {
		h.logger.Warn("get_sub_link: resolve failed",
			zap.Int64("tg_id", telegramID), zap.Bool("registered", registered), zap.Error(err))
		h.answerCallback(ctx, callback.ID, "❌ Откройте приложение, чтобы активировать аккаунт", true)
		return
	}

	tok, err := h.vpnClient.GetSubscriptionToken(ctx, userID)
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			h.answerCallback(ctx, callback.ID,
				"❌ Подписки нет. Сначала оплати тариф.", true)
			return
		}
		h.logger.Error("get_sub_link: GetSubscriptionToken failed",
			zap.Int64("tg_id", telegramID), zap.Int64("user_id", userID), zap.Error(err))
		h.answerCallback(ctx, callback.ID, "❌ Не удалось получить ссылку", true)
		return
	}
	if tok == nil || tok.SubscriptionToken == "" {
		h.answerCallback(ctx, callback.ID, "❌ Не удалось получить ссылку", true)
		return
	}

	// Собираем публичный URL: <PUBLIC_BASE_URL or webAppRootURL>/api/v1/subscription/<token>.
	// HTTP-handler /vpn/subscription-token делает то же самое (см. handler/vpn.go),
	// но он берёт baseURL из http.Request (X-Forwarded-Host fallback). У бота
	// http.Request'а нет — берём только env, а fallback'имся на тот же
	// prod-домен, что и Mini App (webAppRootURL).
	baseURL := os.Getenv("PUBLIC_BASE_URL")
	if baseURL == "" {
		baseURL = webAppRootURL
	}
	subURL := fmt.Sprintf("%s/api/v1/subscription/%s", baseURL, tok.SubscriptionToken)

	// Текст инструкции — короткий, по шагам. Логика гайда повторяет страницу
	// Mini App `/connect` (Happ как дефолтный клиент с авто-импортом по URL).
	// Custom-схема happ:// в Telegram inline-кнопки не пускает (только http(s)),
	// поэтому импорт через "+" в самом приложении — копируем ссылку, вставляем.
	newText := fmt.Sprintf(
		"🔑 <b>Твой VPN-ключ</b>\n"+
			"─────────\n"+
			"<code>%s</code>\n"+
			"<i>Тапни по ссылке выше, чтобы скопировать.</i>\n\n"+
			"📥 <b>Как подключиться</b> (через <b>Happ</b>):\n"+
			"1. Скачай Happ для своей платформы — кнопка ниже.\n"+
			"2. Открой Happ → нажми <b>«+»</b> в правом верхнем углу → "+
			"вставь скопированную ссылку → <b>«Добавить»</b>.\n"+
			"3. Нажми большую кнопку включения в центре → выбери сервер из списка.\n\n"+
			"💡 Ссылка обновляется автоматически — серверы будут добавляться сами.",
		subURL,
	)

	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			// Главный CTA — открыть Mini App с пошаговой инструкцией и
			// авто-импортом через системный deeplink (там happ:// работает).
			{
				{Text: "📱 Подключиться через приложение", WebApp: &telegram.WebAppInfo{URL: webAppConnectURL}},
			},
			// Сторы Happ по платформам — 2 ряда по 2 кнопки. Layout соответствует
			// привычному порядку «mobile → desktop».
			{
				{Text: "🤖 Android", URL: happStoreAndroid},
				{Text: "🍎 iOS", URL: happStoreIOS},
			},
			{
				{Text: "🪟 Windows", URL: happStoreWindows},
				{Text: "💻 macOS", URL: happStoreMacOS},
			},
		},
	}
	if err := h.telegramClient.EditMessageText(ctx, telegram.EditMessageTextParams{
		ChatID:      callback.Message.Chat.ID,
		MessageID:   callback.Message.MessageID,
		Text:        newText,
		ParseMode:   "HTML",
		ReplyMarkup: kb,
	}); err != nil {
		h.logger.Warn("get_sub_link: edit message failed",
			zap.Int64("chat_id", callback.Message.Chat.ID), zap.Error(err))
	}
	h.answerCallback(ctx, callback.ID, "Ссылка получена", false)
}

// ─── /🛒 Купить подписку ──────────────────────────────────────────

// handleBuyButton — text-match на "🛒 Купить подписку" + callback `cbBuyPrompt`.
// Берём из ListPlans (active=true) видимые планы (без is_trial и без promo
// id=101), сортируем по duration_days, считаем скидки относительно 1мес и
// рендерим карточку с двумя inline: quick-buy на 1мес + Mini App /plans.
func (h *TelegramBotHandler) handleBuyButton(ctx context.Context, chatID, telegramID int64) {
	if h.subscriptionClient == nil {
		_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: chatID, Text: "❌ Тарифы временно недоступны.",
		})
		return
	}
	resp, err := h.subscriptionClient.ListPlans(ctx, true)
	if err != nil || resp == nil || len(resp.Plans) == 0 {
		h.logger.Error("buy button: ListPlans failed",
			zap.Int64("tg_id", telegramID), zap.Error(err))
		_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Не удалось загрузить тарифы. Попробуй позже или открой приложение.",
		})
		return
	}

	plans := filterVisiblePlans(resp.Plans)
	if len(plans) == 0 {
		h.logger.Warn("buy button: no visible plans after filter")
		_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Тарифы временно недоступны. Открой приложение или попробуй позже.",
		})
		return
	}

	text, quickPlan := renderPlansText(plans)
	if quickPlan == nil {
		// Нет 1-месячного — fallback: только кнопка Mini App.
		kb := &telegram.InlineKeyboardMarkup{
			InlineKeyboard: [][]telegram.InlineKeyboardButton{{
				{Text: "📱 Открыть приложение", WebApp: &telegram.WebAppInfo{URL: webAppPlansURL}},
			}},
		}
		_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: chatID, Text: text, ParseMode: "HTML", ReplyMarkup: kb,
		})
		return
	}

	quickText := fmt.Sprintf("💳 1мес — %s₽", formatRub(quickPlan.BasePrice))
	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{{
			{Text: quickText, CallbackData: fmt.Sprintf("%s%d", cbBuyQuickPrefix, quickPlan.Id)},
			{Text: "📱 Все тарифы", WebApp: &telegram.WebAppInfo{URL: webAppPlansURL}},
		}},
	}
	_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   "HTML",
		ReplyMarkup: kb,
	})
}

// filterVisiblePlans — только активные плательные планы, без trial и без
// promo (id=101). Сортирует по duration_days ASC.
func filterVisiblePlans(plans []*subpb.SubscriptionPlan) []*subpb.SubscriptionPlan {
	out := make([]*subpb.SubscriptionPlan, 0, len(plans))
	for _, p := range plans {
		if p == nil || !p.IsActive || p.IsTrial || p.Id == promoPlanIDExcluded {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].DurationDays < out[j].DurationDays
	})
	return out
}

// renderPlansText строит сам текст карточки тарифов и попутно находит
// 1-месячный план для quick-buy. Скидки считаются относительно цены
// 1-месячного × число месяцев. Если 1-месячного нет — quickPlan=nil.
func renderPlansText(plans []*subpb.SubscriptionPlan) (string, *subpb.SubscriptionPlan) {
	var quickPlan *subpb.SubscriptionPlan
	var basePerMonth float64
	for _, p := range plans {
		if p.DurationDays == 30 {
			quickPlan = p
			basePerMonth, _ = strconv.ParseFloat(p.BasePrice, 64)
			break
		}
	}

	var b strings.Builder
	b.WriteString("🛒 <b>Тарифы</b>\n─────────\n")
	for _, p := range plans {
		months := int(p.DurationDays / 30)
		priceN, _ := strconv.ParseFloat(p.BasePrice, 64)

		discount := ""
		if months > 1 && basePerMonth > 0 {
			full := basePerMonth * float64(months)
			if priceN > 0 && priceN < full {
				pct := int(math.Round((1 - priceN/full) * 100))
				if pct > 0 {
					discount = fmt.Sprintf(" (-%d%%)", pct)
				}
			}
		}
		star := ""
		if months == 12 {
			star = " ⭐"
		}
		fmt.Fprintf(&b, "• %s — <b>%s ₽</b>%s%s\n",
			humanDuration(p.DurationDays), formatRub(p.BasePrice), discount, star)
	}
	b.WriteString("\n<i>Базово 2 устройства · можно расширить в приложении</i>")
	return b.String(), quickPlan
}

// humanDuration — "1 месяц" / "3 месяца" / "12 месяцев".
// Считаем от месяцев (30 дней). Не-кратные 30 (теоретически) рендерим как
// "N дней" — fallback на случай редких планов.
func humanDuration(days int32) string {
	if days%30 == 0 && days > 0 {
		months := days / 30
		var word string
		switch {
		case months == 1:
			word = "месяц"
		case months >= 2 && months <= 4:
			word = "месяца"
		default:
			word = "месяцев"
		}
		return fmt.Sprintf("%d %s", months, word)
	}
	return fmt.Sprintf("%d дн.", days)
}

// formatRub — превращает "199.00" / "199" в "199" (без копеек, если их нет).
// Wata/база хранят цену как Decimal-строку, у нас всегда целые рубли.
func formatRub(s string) string {
	if i := strings.Index(s, "."); i != -1 {
		return s[:i]
	}
	return s
}

// handleBuyQuick — callback `buy_quick_<plan_id>`. Создаёт wata-инвойс,
// edit-ит сообщение в "К оплате X₽" с URL-кнопкой "Оплатить" и "Отмена".
func (h *TelegramBotHandler) handleBuyQuick(ctx context.Context, callback *CallbackQuery, planIDStr string) {
	if h.paymentClient == nil || callback.Message == nil {
		h.answerCallback(ctx, callback.ID, "❌ Платежи недоступны", true)
		return
	}
	planID64, err := strconv.ParseInt(planIDStr, 10, 32)
	if err != nil || planID64 <= 0 {
		h.answerCallback(ctx, callback.ID, "❌ Некорректный план", true)
		return
	}
	planID := int32(planID64)

	telegramID := callback.From.ID
	userID, registered, err := h.resolveUserIDForBot(ctx, telegramID)
	if err != nil {
		h.logger.Error("buy_quick: resolve user failed",
			zap.Int64("tg_id", telegramID), zap.Error(err))
		h.answerCallback(ctx, callback.ID, "❌ Не удалось создать счёт", true)
		return
	}
	if !registered {
		h.answerCallback(ctx, callback.ID,
			"❌ Сначала открой приложение, чтобы активировать аккаунт", true)
		return
	}

	inv, err := h.paymentClient.CreateInvoice(ctx, userID, planID, botBuyDefaultMaxDevices, botBuyDefaultProvider)
	if err != nil || inv == nil || inv.InvoiceLink == "" {
		h.logger.Error("buy_quick: CreateInvoice failed",
			zap.Int64("tg_id", telegramID),
			zap.Int64("user_id", userID),
			zap.Int32("plan_id", planID),
			zap.Error(err))
		h.answerCallback(ctx, callback.ID, "❌ Не удалось создать счёт", true)
		return
	}

	priceText := h.lookupPlanPrice(ctx, planID)
	priceLabel := "оплате"
	if priceText != "" {
		priceLabel = "оплате " + priceText + "₽"
	}
	newText := fmt.Sprintf(
		"💳 <b>Счёт создан</b>\n─────────\n"+
			"К %s\n⏰ Активен %d минут\n\n"+
			"После оплаты подписка активируется автоматически.",
		priceLabel, botInvoiceTTLMinutes,
	)
	payText := "💳 Оплатить"
	if priceText != "" {
		payText = fmt.Sprintf("💳 Оплатить %s₽", priceText)
	}
	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{{
			{Text: payText, URL: inv.InvoiceLink},
			{Text: "❌ Отмена", CallbackData: cbCancelInvoice},
		}},
	}
	if err := h.telegramClient.EditMessageText(ctx, telegram.EditMessageTextParams{
		ChatID:      callback.Message.Chat.ID,
		MessageID:   callback.Message.MessageID,
		Text:        newText,
		ParseMode:   "HTML",
		ReplyMarkup: kb,
	}); err != nil {
		h.logger.Warn("buy_quick: edit message failed",
			zap.Int64("chat_id", callback.Message.Chat.ID), zap.Error(err))
	}
	h.logger.Info("buy_quick: invoice created",
		zap.Int64("tg_id", telegramID),
		zap.Int64("user_id", userID),
		zap.Int32("plan_id", planID),
		zap.Int64("payment_id", inv.PaymentId))
	h.answerCallback(ctx, callback.ID, "Счёт создан", false)
}

// lookupPlanPrice достаёт цену плана для текста кнопки/сообщения. Если
// ListPlans упадёт или планa нет — возвращаем "" и UI рендерит без суммы
// (платежная страница сама покажет). Не критично для функциональности,
// поэтому без обработки ошибок наружу.
func (h *TelegramBotHandler) lookupPlanPrice(ctx context.Context, planID int32) string {
	if h.subscriptionClient == nil {
		return ""
	}
	resp, err := h.subscriptionClient.ListPlans(ctx, true)
	if err != nil || resp == nil {
		return ""
	}
	for _, p := range resp.Plans {
		if p != nil && p.Id == planID {
			return formatRub(p.BasePrice)
		}
	}
	return ""
}

// handleCancelInvoice — callback `cancel_invoice`. Просто заменяет
// сообщение со счётом на "отменено" с инвитом нажать «🛒 Купить подписку».
// Никакой логики на стороне payment-service не делаем — invoice истечёт
// сам по TTL, что эквивалентно отмене.
func (h *TelegramBotHandler) handleCancelInvoice(ctx context.Context, callback *CallbackQuery) {
	if callback.Message == nil {
		h.answerCallback(ctx, callback.ID, "", false)
		return
	}
	newText := "❌ Оплата отменена.\n\nЧтобы попробовать снова — нажми «🛒 Купить подписку» внизу."
	if err := h.telegramClient.EditMessageText(ctx, telegram.EditMessageTextParams{
		ChatID:    callback.Message.Chat.ID,
		MessageID: callback.Message.MessageID,
		Text:      newText,
		ParseMode: "HTML",
	}); err != nil {
		h.logger.Warn("cancel_invoice: edit message failed",
			zap.Int64("chat_id", callback.Message.Chat.ID), zap.Error(err))
	}
	h.answerCallback(ctx, callback.ID, "Отменено", false)
}

// ─── helpers ──────────────────────────────────────────────────────

// sendOpenAppPrompt — короткое сообщение для ситуаций, когда юзер ещё не
// открыл Mini App. Текст параметризован, кнопка одна — открыть Mini App.
func (h *TelegramBotHandler) sendOpenAppPrompt(ctx context.Context, chatID int64, text string) {
	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{{
			{Text: "📱 Открыть приложение", WebApp: &telegram.WebAppInfo{URL: webAppRootURL}},
		}},
	}
	_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
		ChatID: chatID, Text: text, ParseMode: "HTML", ReplyMarkup: kb,
	})
}
