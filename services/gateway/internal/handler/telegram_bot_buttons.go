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

// instructionPostURL — пост в TG-канале @maydavpn с пошаговой инструкцией
// по подключению. Кнопка «📖 Как подключиться» в карточке статуса (после
// нажатия «🌐 Подключиться») ведёт сюда. Если пост поменяется —
// либо обновить константу, либо переопределить через env INSTRUCTION_POST_URL.
const instructionPostURL = "https://t.me/maydavpn/14"

// resolveInstructionPostURL — env-override для instructionPostURL.
// Возвращает значение из ENV INSTRUCTION_POST_URL если оно непустое,
// иначе захардкоженный default. Вынесено в функцию а не в init() чтобы
// тесты могли подменить env прямо перед вызовом без перезапуска процесса.
func resolveInstructionPostURL() string {
	if v := os.Getenv("INSTRUCTION_POST_URL"); v != "" {
		return v
	}
	return instructionPostURL
}

// Callback-data префиксы для inline-кнопок, которые рисуют handleConnectButton/
// handleBuyButton. Длина < 64 байт (лимит Telegram).
//
// cbConnectPrompt / cbBuyPrompt продублированы в welcome-сообщении (см.
// sendStartMessage), чтобы юзер мог открыть статус/тарифы прямо из welcome
// без необходимости тапать reply-кнопку внизу. Оба callback'а ведут на те
// же хендлеры, что и text-match'и reply-кнопок.
//
// Buy-flow (3-step picker):
//   buy_plan_<plan_id>                — юзер выбрал план → показываем устройства
//   buy_confirm_<plan_id>_<devices>   — юзер выбрал кол-во устройств → CreateInvoice
//   buy_back_plans                    — назад со страницы устройств в список планов
//   buy_back_dev_<plan_id>            — назад со страницы invoice в выбор устройств
//   cbBuyQuickPrefix                  — DEPRECATED, оставлен ради старых сообщений.
const (
	cbGetSubLink           = "get_sub_link"
	cbConnectPrompt        = "connect_prompt"
	cbBuyPrompt            = "buy_prompt"
	cbBuyQuickPrefix       = "buy_quick_"   // deprecated (см. buy_confirm_)
	cbBuyPlanPrefix        = "buy_plan_"
	cbBuyConfirmPrefix     = "buy_confirm_"
	cbBuyBackPlans         = "buy_back_plans"
	cbBuyBackDevicesPrefix = "buy_back_dev_"
	cbCancelInvoice        = "cancel_invoice"
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

// botBuyDefaultProvider — провайдер для покупки из бота. Должен быть
// рублёвым с URL-инвойсом (юзер кликает прямо в TG → открывается оплатная
// страница). Telegram Stars не подходит: для Stars нужен Mini App
// `openInvoice()`, а в чисто бот-flow его нет.
//
// На проде сейчас включён `platega` (см. payment-service WATA_ENABLED /
// PLATEGA_ENABLED). Если в будущем дефолт сменится — поправить здесь
// и/или вытащить в env BOT_BUY_PROVIDER. Имя провайдера должно совпадать
// с тем, под которым он зарегистрирован в payment-service (см. service/
// payment.go register loop).
const botBuyDefaultProvider = "platega"

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
		"Твой VPN-ключ\n"+
			"<pre>%s</pre>\n"+
			"<i>Тапни по ссылке выше, чтобы скопировать.</i>\n\n"+
			"Подключись через Happ/INCY...",
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

	instructionURL := resolveInstructionPostURL()

	// Нет активной подписки → CTA на тарифы.
	if subResp == nil || !subResp.HasActive || subResp.Subscription == nil {
		text := "📡 У тебя пока нет активной подписки.\n\nВыбери тариф — получишь ключ сразу после оплаты."
		kb := &telegram.InlineKeyboardMarkup{
			InlineKeyboard: [][]telegram.InlineKeyboardButton{
				{
					{Text: "💳 К тарифам", CallbackData: cbBuyPrompt},
					{Text: "📱 Открыть приложение", WebApp: &telegram.WebAppInfo{URL: webAppPlansURL}},
				},
				{
					{Text: "📖 Как подключиться", URL: instructionURL},
				},
			},
		}
		_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
			ChatID:      chatID,
			Text:        text,
			ParseMode:   "HTML",
			ReplyMarkup: kb,
		})
		return
	}

	// Активная подписка → карточка статуса. Три inline-кнопки в два ряда:
	// верх — два главных action'а (Mini App connect + получить ссылку),
	// низ — отдельной строкой ссылка на инструкцию в канале @maydavpn.
	text := formatSubscriptionCard(subResp.Subscription, telegramID)
	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{
				{Text: "📱 Подключиться", WebApp: &telegram.WebAppInfo{URL: webAppConnectURL}},
				{Text: "🔑 Получить ссылку", CallbackData: cbGetSubLink},
			},
			{
				{Text: "📖 Как подключиться", URL: instructionURL},
			},
		},
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
			"<pre>%s</pre>\n"+
			"<i>Тапни по ссылке выше, чтобы скопировать.</i>\n\n"+
			"📥 <b>Как подключиться</b> (через <b>Happ/INCY</b>):\n"+
			"1. Скачай приложение для своей платформы — кнопки ниже.\n"+
			"2. В приложении нажми <b>«+»</b> → вставь скопированную ссылку → <b>«Добавить»</b>.\n"+
			"3. Нажми кнопку включения → выбери сервер из списка.\n\n"+
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

	text, kb := buildPlansMenu(plans)
	_ = h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   "HTML",
		ReplyMarkup: kb,
	})
}

// buildPlansMenu — общий рендер для handleBuyButton (новое сообщение) и
// handleBuyBackToPlans (edit существующего). Для каждого видимого плана —
// одна inline-кнопка с базовой ценой (2 устройства) и %-скидкой
// относительно ставки 1мес × число месяцев. Сортировка по duration_days
// уже выставлена filterVisiblePlans.
func buildPlansMenu(plans []*subpb.SubscriptionPlan) (string, *telegram.InlineKeyboardMarkup) {
	const heading = "🛒 <b>Выбери тариф</b>\n─────────\n" +
		"<i>Базовая цена показана для 2 устройств. На следующем шаге выберешь сколько устройств подключить.</i>"

	var basePerMonth float64
	for _, p := range plans {
		if p.DurationDays == 30 {
			basePerMonth, _ = strconv.ParseFloat(p.BasePrice, 64)
			break
		}
	}

	rows := make([][]telegram.InlineKeyboardButton, 0, len(plans))
	for _, p := range plans {
		months := int(p.DurationDays / 30)
		priceN, _ := strconv.ParseFloat(p.BasePrice, 64)

		label := humanDuration(p.DurationDays) + " · " + formatRub(p.BasePrice) + " ₽"
		if months > 1 && basePerMonth > 0 {
			full := basePerMonth * float64(months)
			if priceN > 0 && priceN < full {
				if pct := int(math.Round((1 - priceN/full) * 100)); pct > 0 {
					label += fmt.Sprintf(" -%d%%", pct)
				}
			}
		}
		if months == 12 {
			label += " ⭐"
		}
		rows = append(rows, []telegram.InlineKeyboardButton{{
			Text:         label,
			CallbackData: fmt.Sprintf("%s%d", cbBuyPlanPrefix, p.Id),
		}})
	}
	return heading, &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
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

// renderPlansText — старый рендер «список тарифов плюс quick-buy 1мес»;
// заменён на buildPlansMenu в task #ребилд бот-флоу. Файл оставлен на тот
// случай если в будущем понадобится текст-сводка без интерактива (для
// /help / админки), и чтобы у тестов TestHandleBuyButton_RendersPlans
// не было спайдер-импортов.
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
				if pct := int(math.Round((1 - priceN/full) * 100)); pct > 0 {
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

// pluralizeDays — "1 день" / "3 дня" / "30 дней" по правилам русского языка.
// Используется в welcome-сообщении /start для динамической длительности
// триала (см. task 19 — override через campaigns.trial_duration_days).
//
// Алгоритм классический:
//   - Последние 2 цифры 11..19 → "дней" (исключение из правила единиц)
//   - Иначе по последней цифре: 1 → "день", 2-4 → "дня", 0/5-9 → "дней"
func pluralizeDays(n int) string {
	if n < 0 {
		n = -n
	}
	mod100 := n % 100
	mod10 := n % 10
	switch {
	case mod100 >= 11 && mod100 <= 19:
		return fmt.Sprintf("%d дней", n)
	case mod10 == 1:
		return fmt.Sprintf("%d день", n)
	case mod10 >= 2 && mod10 <= 4:
		return fmt.Sprintf("%d дня", n)
	default:
		return fmt.Sprintf("%d дней", n)
	}
}

// formatRub — превращает "199.00" / "199" в "199" (без копеек, если их нет).
// Wata/база хранят цену как Decimal-строку, у нас всегда целые рубли.
func formatRub(s string) string {
	if i := strings.Index(s, "."); i != -1 {
		return s[:i]
	}
	return s
}

// handleBuyQuick — DEPRECATED, см. cbBuyQuickPrefix. Перенаправляет на
// новый buy-flow с дефолтным числом устройств, чтобы старые сообщения
// (из истории чата с прошлой версии) не висели мёртвыми кнопками.
func (h *TelegramBotHandler) handleBuyQuick(ctx context.Context, callback *CallbackQuery, planIDStr string) {
	planID64, err := strconv.ParseInt(planIDStr, 10, 32)
	if err != nil || planID64 <= 0 {
		h.answerCallback(ctx, callback.ID, "❌ Некорректный план", true)
		return
	}
	h.handleBuyConfirm(ctx, callback, int32(planID64), botBuyDefaultMaxDevices)
}

// handleBuyPlan — callback "buy_plan_<plan_id>". Юзер выбрал тариф (1мес/
// 3мес/...), показываем меню выбора количества устройств. Цены берутся из
// device_addon_pricing (subscriptionClient.GetDevicePricing). Внизу —
// кнопка «◀️ Назад» возвращает на список планов (handleBuyBackToPlans).
func (h *TelegramBotHandler) handleBuyPlan(ctx context.Context, callback *CallbackQuery, planIDStr string) {
	if callback.Message == nil {
		h.answerCallback(ctx, callback.ID, "", false)
		return
	}
	planID64, err := strconv.ParseInt(planIDStr, 10, 32)
	if err != nil || planID64 <= 0 {
		h.answerCallback(ctx, callback.ID, "❌ Некорректный план", true)
		return
	}
	planID := int32(planID64)

	plan, err := h.lookupPlan(ctx, planID)
	if err != nil || plan == nil {
		h.logger.Warn("buy_plan: plan lookup failed",
			zap.Int32("plan_id", planID), zap.Error(err))
		h.answerCallback(ctx, callback.ID, "❌ Тариф не найден", true)
		return
	}

	pricing, err := h.subscriptionClient.GetDevicePricing(ctx, planID)
	if err != nil || pricing == nil || len(pricing.Prices) == 0 {
		h.logger.Error("buy_plan: GetDevicePricing failed",
			zap.Int32("plan_id", planID), zap.Error(err))
		h.answerCallback(ctx, callback.ID, "❌ Не удалось загрузить цены устройств", true)
		return
	}

	// Сортируем по max_devices ASC — проще читать (2,3,4,5,10).
	prices := append([]*subpb.DevicePrice(nil), pricing.Prices...)
	sort.Slice(prices, func(i, j int) bool { return prices[i].MaxDevices < prices[j].MaxDevices })

	text := fmt.Sprintf(
		"📱 <b>%s · %s ₽</b>\n─────────\n"+
			"Сколько устройств подключить?\n"+
			"<i>Цена меняется в зависимости от количества устройств.</i>",
		humanDuration(plan.DurationDays),
		formatRub(plan.BasePrice),
	)

	// 2 кнопки в ряд для компактности (на мобиле 2-широких помещаются).
	rows := make([][]telegram.InlineKeyboardButton, 0, len(prices)/2+2)
	row := make([]telegram.InlineKeyboardButton, 0, 2)
	for _, p := range prices {
		if p == nil {
			continue
		}
		btn := telegram.InlineKeyboardButton{
			Text:         fmt.Sprintf("%d %s · %s ₽", p.MaxDevices, deviceWord(p.MaxDevices), formatRub(p.Price)),
			CallbackData: fmt.Sprintf("%s%d_%d", cbBuyConfirmPrefix, planID, p.MaxDevices),
		}
		row = append(row, btn)
		if len(row) == 2 {
			rows = append(rows, row)
			row = make([]telegram.InlineKeyboardButton, 0, 2)
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []telegram.InlineKeyboardButton{
		{Text: "◀️ Назад к тарифам", CallbackData: cbBuyBackPlans},
	})

	if err := h.telegramClient.EditMessageText(ctx, telegram.EditMessageTextParams{
		ChatID:      callback.Message.Chat.ID,
		MessageID:   callback.Message.MessageID,
		Text:        text,
		ParseMode:   "HTML",
		ReplyMarkup: &telegram.InlineKeyboardMarkup{InlineKeyboard: rows},
	}); err != nil {
		h.logger.Warn("buy_plan: edit message failed",
			zap.Int64("chat_id", callback.Message.Chat.ID), zap.Error(err))
	}
	h.answerCallback(ctx, callback.ID, "", false)
}

// handleBuyConfirm — callback "buy_confirm_<plan_id>_<max_devices>".
// Финальный шаг: создаём wata-инвойс, edit-им сообщение в «Счёт создан»
// с URL-кнопкой «💳 Оплатить» + «◀️ Назад к устройствам» + «❌ Отмена».
func (h *TelegramBotHandler) handleBuyConfirm(ctx context.Context, callback *CallbackQuery, planID, maxDevices int32) {
	if h.paymentClient == nil || callback.Message == nil {
		h.answerCallback(ctx, callback.ID, "❌ Платежи недоступны", true)
		return
	}
	if planID <= 0 || maxDevices <= 0 {
		h.answerCallback(ctx, callback.ID, "❌ Некорректные параметры", true)
		return
	}

	telegramID := callback.From.ID
	userID, registered, err := h.resolveUserIDForBot(ctx, telegramID)
	if err != nil {
		h.logger.Error("buy_confirm: resolve user failed",
			zap.Int64("tg_id", telegramID), zap.Error(err))
		h.answerCallback(ctx, callback.ID, "❌ Не удалось создать счёт", true)
		return
	}
	if !registered {
		h.answerCallback(ctx, callback.ID,
			"❌ Сначала открой приложение, чтобы активировать аккаунт", true)
		return
	}

	inv, err := h.paymentClient.CreateInvoice(ctx, userID, planID, maxDevices, botBuyDefaultProvider)
	if err != nil || inv == nil || inv.InvoiceLink == "" {
		h.logger.Error("buy_confirm: CreateInvoice failed",
			zap.Int64("tg_id", telegramID),
			zap.Int64("user_id", userID),
			zap.Int32("plan_id", planID),
			zap.Int32("max_devices", maxDevices),
			zap.Error(err))
		h.answerCallback(ctx, callback.ID, "❌ Не удалось создать счёт", true)
		return
	}

	priceText := h.lookupDevicePrice(ctx, planID, maxDevices)
	planName := h.lookupPlanName(ctx, planID)
	header := "💳 <b>Счёт создан</b>"
	if planName != "" && priceText != "" {
		header = fmt.Sprintf("💳 <b>%s · %d %s · %s ₽</b>",
			planName, maxDevices, deviceWord(maxDevices), priceText)
	}
	newText := fmt.Sprintf(
		"%s\n─────────\n"+
			"⏰ Счёт активен %d минут\n\n"+
			"После оплаты подписка активируется автоматически.",
		header, botInvoiceTTLMinutes,
	)
	payText := "💳 Оплатить"
	if priceText != "" {
		payText = fmt.Sprintf("💳 Оплатить %s ₽", priceText)
	}
	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{{Text: payText, URL: inv.InvoiceLink}},
			{
				{Text: "◀️ Назад", CallbackData: fmt.Sprintf("%s%d", cbBuyBackDevicesPrefix, planID)},
				{Text: "❌ Отмена", CallbackData: cbCancelInvoice},
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
		h.logger.Warn("buy_confirm: edit message failed",
			zap.Int64("chat_id", callback.Message.Chat.ID), zap.Error(err))
	}
	h.logger.Info("buy_confirm: invoice created",
		zap.Int64("tg_id", telegramID),
		zap.Int64("user_id", userID),
		zap.Int32("plan_id", planID),
		zap.Int32("max_devices", maxDevices),
		zap.Int64("payment_id", inv.PaymentId))
	h.answerCallback(ctx, callback.ID, "Счёт создан", false)
}

// handleBuyBackToPlans — callback "buy_back_plans". Edit-ит текущее
// сообщение обратно в список тарифов (тот же что показал handleBuyButton).
func (h *TelegramBotHandler) handleBuyBackToPlans(ctx context.Context, callback *CallbackQuery) {
	if callback.Message == nil {
		h.answerCallback(ctx, callback.ID, "", false)
		return
	}
	if h.subscriptionClient == nil {
		h.answerCallback(ctx, callback.ID, "❌ Тарифы недоступны", true)
		return
	}
	resp, err := h.subscriptionClient.ListPlans(ctx, true)
	if err != nil || resp == nil {
		h.answerCallback(ctx, callback.ID, "❌ Не удалось загрузить тарифы", true)
		return
	}
	plans := filterVisiblePlans(resp.Plans)
	if len(plans) == 0 {
		h.answerCallback(ctx, callback.ID, "❌ Тарифы временно недоступны", true)
		return
	}
	text, kb := buildPlansMenu(plans)
	if err := h.telegramClient.EditMessageText(ctx, telegram.EditMessageTextParams{
		ChatID:      callback.Message.Chat.ID,
		MessageID:   callback.Message.MessageID,
		Text:        text,
		ParseMode:   "HTML",
		ReplyMarkup: kb,
	}); err != nil {
		h.logger.Warn("buy_back_plans: edit message failed",
			zap.Int64("chat_id", callback.Message.Chat.ID), zap.Error(err))
	}
	h.answerCallback(ctx, callback.ID, "", false)
}

// lookupPlan возвращает SubscriptionPlan по id (через ListPlans). Не
// критично для функциональности — для текста (имя/цена). Если упало —
// возвращаем nil без ошибки наружу, юзер увидит generic-текст.
func (h *TelegramBotHandler) lookupPlan(ctx context.Context, planID int32) (*subpb.SubscriptionPlan, error) {
	if h.subscriptionClient == nil {
		return nil, fmt.Errorf("subscription client not configured")
	}
	resp, err := h.subscriptionClient.ListPlans(ctx, true)
	if err != nil || resp == nil {
		return nil, err
	}
	for _, p := range resp.Plans {
		if p != nil && p.Id == planID {
			return p, nil
		}
	}
	return nil, nil
}

// lookupPlanName — короткая обёртка для текста сообщения.
func (h *TelegramBotHandler) lookupPlanName(ctx context.Context, planID int32) string {
	p, err := h.lookupPlan(ctx, planID)
	if err != nil || p == nil {
		return ""
	}
	return humanDuration(p.DurationDays)
}

// lookupDevicePrice — цена за выбранный (plan_id, max_devices) для текста
// сообщения «к оплате X ₽». Через GetDevicePricing — там же где брали
// цены для кнопок выбора устройств.
func (h *TelegramBotHandler) lookupDevicePrice(ctx context.Context, planID, maxDevices int32) string {
	if h.subscriptionClient == nil {
		return ""
	}
	resp, err := h.subscriptionClient.GetDevicePricing(ctx, planID)
	if err != nil || resp == nil {
		return ""
	}
	for _, p := range resp.Prices {
		if p != nil && p.MaxDevices == maxDevices {
			return formatRub(p.Price)
		}
	}
	return ""
}

// deviceWord — склонение числительного для слова «устройство»:
// 1, 21, 31 → "устройство", 2-4, 22-24 → "устройства", 5-20, 25-30 → "устройств".
func deviceWord(n int32) string {
	mod100 := n % 100
	mod10 := n % 10
	if mod100 >= 11 && mod100 <= 14 {
		return "устройств"
	}
	switch mod10 {
	case 1:
		return "устройство"
	case 2, 3, 4:
		return "устройства"
	default:
		return "устройств"
	}
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
