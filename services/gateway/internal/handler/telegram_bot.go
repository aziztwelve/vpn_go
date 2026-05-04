package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vpn/gateway/internal/client"
	"github.com/vpn/platform/pkg/telegram"
	pb "github.com/vpn/shared/pkg/proto/subscription/v1"
	"go.uber.org/zap"
)

// refStartParamPrefix — префикс start-параметра для реферального deep-link.
// Формат ссылки: https://t.me/<bot>?start=ref_<token>
// Telegram передаёт боту "ref_<token>" в команде "/start ref_<token>".
const refStartParamPrefix = "ref_"

// srcStartParamPrefix — префикс для deep-link маркетинговой воронки (кампании).
// Формат: https://t.me/<bot>?start=src_<slug>
// Атрибуция к кампании — независима от реферальной программы (ref_).
const srcStartParamPrefix = "src_"

// TelegramBotHandler обрабатывает команды и callback'и от Telegram бота.
// broadcastClient может быть nil — тогда retention-callback'и (bc_*) будут
// игнорироваться (полезно для dev окружений без auth-service'а).
type TelegramBotHandler struct {
	telegramClient     *telegram.Client
	subscriptionClient *client.SubscriptionClient
	authClient         *client.AuthClient
	broadcastClient    *client.BroadcastClient
	logger             *zap.Logger
	channelUsername    string
}

func NewTelegramBotHandler(
	telegramClient *telegram.Client,
	subscriptionClient *client.SubscriptionClient,
	authClient *client.AuthClient,
	broadcastClient *client.BroadcastClient,
	logger *zap.Logger,
	channelUsername string,
) *TelegramBotHandler {
	return &TelegramBotHandler{
		telegramClient:     telegramClient,
		subscriptionClient: subscriptionClient,
		authClient:         authClient,
		broadcastClient:    broadcastClient,
		logger:             logger,
		channelUsername:    channelUsername,
	}
}

// Update представляет Telegram webhook update
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      *Chat  `json:"chat"`
	Text      string `json:"text,omitempty"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

type Chat struct {
	ID int64 `json:"id"`
}

// HandleBotWebhook обрабатывает webhook от Telegram бота
func (h *TelegramBotHandler) HandleBotWebhook(w http.ResponseWriter, r *http.Request) {
	var update Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		h.logger.Error("Failed to decode telegram update", zap.Error(err))
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Обработка команд
	if update.Message != nil && update.Message.Text != "" {
		h.handleCommand(ctx, update.Message)
	}

	// Обработка callback'ов от inline кнопок
	if update.CallbackQuery != nil {
		h.handleCallback(ctx, update.CallbackQuery)
	}

	w.WriteHeader(http.StatusOK)
}

// handleCommand обрабатывает текстовые команды.
//
// /start поддерживает опциональный параметр (deep-link):
//
//	/start                — обычный приветственный экран
//	/start ref_<token>    — реферальная ссылка, сохраняем pending атрибуцию
//	/start <other>        — игнорируем параметр, шлём приветствие
func (h *TelegramBotHandler) handleCommand(ctx context.Context, msg *Message) {
	text := strings.TrimSpace(msg.Text)

	// Разбираем "/start" или "/start@maydavpnbot" с опциональным параметром.
	cmd, param, _ := strings.Cut(text, " ")
	cmd = strings.TrimSpace(cmd)
	param = strings.TrimSpace(param)

	switch cmd {
	case "/start", "/start@maydavpnbot":
		h.handleStart(ctx, msg.Chat.ID, msg.From.ID, param, msg.From)
	case "/bonus", "/bonus@maydavpnbot":
		h.sendBonusMessage(ctx, msg.Chat.ID)
	}
}

// handleStart обрабатывает /start [param]. Поддерживаемые параметры:
//
//	ref_<token> — реферальная ссылка (персональная от юзера)
//	src_<slug>  — маркетинговая воронка/кампания (от админа для блогера)
//
// Оба варианта независимы: юзер может прийти по ref'у и это не мешает
// одновременной атрибуции к кампании (разные pending-таблицы).
// Атрибуция окончательно запишется в ValidateTelegramUser при первом открытии Mini App.
//
// Также фиксируем сам факт нажатия /start в bot_starts (воронка бот → Mini App).
// Telegram update не содержит last_name, поэтому передаём только username/first_name.
func (h *TelegramBotHandler) handleStart(ctx context.Context, chatID, telegramUserID int64, startParam string, from *User) {
	username, firstName := "", ""
	if from != nil {
		username = from.Username
		firstName = from.FirstName
	}

	// Воронка: фиксируем нажатие /start. auth-service сам резолвит src_<slug>
	// в campaign_id при записи в bot_starts. Best-effort.
	if _, _, err := h.authClient.RecordBotStart(ctx, telegramUserID, username, firstName, startParam); err != nil {
		h.logger.Warn("Failed to record bot start",
			zap.Int64("telegram_id", telegramUserID),
			zap.Error(err))
	}

	switch {
	case strings.HasPrefix(startParam, refStartParamPrefix):
		token := strings.TrimPrefix(startParam, refStartParamPrefix)
		if token != "" {
			if err := h.authClient.SetPendingReferral(ctx, telegramUserID, token); err != nil {
				// Best-effort: ошибка не блокирует UX — юзер всё равно получит
				// приветствие и сможет открыть Mini App. Атрибуция просто не сработает.
				h.logger.Warn("Failed to store pending referral",
					zap.Int64("telegram_id", telegramUserID),
					zap.String("ref_token", token),
					zap.Error(err))
			} else {
				h.logger.Info("Pending referral stored from /start",
					zap.Int64("telegram_id", telegramUserID),
					zap.String("ref_token", token))
			}
		}

	case strings.HasPrefix(startParam, srcStartParamPrefix):
		slug := strings.TrimPrefix(startParam, srcStartParamPrefix)
		if slug != "" {
			campaignID, err := h.authClient.SetPendingCampaign(ctx, telegramUserID, slug)
			if err != nil {
				// Best-effort: ошибка не блокирует UX.
				h.logger.Warn("Failed to store pending campaign",
					zap.Int64("telegram_id", telegramUserID),
					zap.String("slug", slug),
					zap.Error(err))
			} else if campaignID == 0 {
				// slug не найден / кампания архивирована — ссылка протухла.
				h.logger.Info("Campaign slug not found or archived",
					zap.Int64("telegram_id", telegramUserID),
					zap.String("slug", slug))
			} else {
				h.logger.Info("Pending campaign stored from /start",
					zap.Int64("telegram_id", telegramUserID),
					zap.String("slug", slug),
					zap.Int64("campaign_id", campaignID))
			}
		}
	}
	h.sendStartMessage(ctx, chatID, telegramUserID)
}

// sendStartMessage отправляет приветственное сообщение с кнопкой открытия Mini App
func (h *TelegramBotHandler) sendStartMessage(ctx context.Context, chatID int64, userID int64) {
	text := `👋 <b>Добро пожаловать в MaydaVPN!</b>

🔒 Быстрый и безопасный VPN для вашей конфиденциальности

✨ <b>Что вы получите:</b>
• 15 дней пробного периода
• Высокая скорость подключения
• Надежная защита данных
• Простое подключение

📢 <b>Подпишитесь на наш канал</b> @maydavpn — там инструкция по подключению, новости и полезные советы!

💬 По всем вопросам пишите в техподдержку — @maydavpn_support

👉 Нажмите кнопку ниже, чтобы начать!`

	keyboard := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{
				{
					Text: "🚀 Открыть приложение",
					WebApp: &telegram.WebAppInfo{
						URL: "https://cdn.osmonai.com",
					},
				},
			},
		},
	}

	err := h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   "HTML",
		ReplyMarkup: keyboard,
	})
	if err != nil {
		h.logger.Error("Failed to send start message",
			zap.Int64("chat_id", chatID),
			zap.Error(err))
	}
}

// sendBonusMessage отправляет сообщение с кнопками для получения бонуса
func (h *TelegramBotHandler) sendBonusMessage(ctx context.Context, chatID int64) {
	text := `🎁 <b>Получите +3 дня к подписке!</b>

Подпишитесь на наш канал и получите бонус:

1️⃣ Нажмите "Подписаться на канал"
2️⃣ Подпишитесь на канал
3️⃣ Вернитесь сюда и нажмите "Проверить подписку"`

	keyboard := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{
				{
					Text: "📢 Подписаться на канал",
					URL:  fmt.Sprintf("https://t.me/%s", h.channelUsername[1:]), // убираем @
				},
			},
			{
				{
					Text:         "✅ Проверить подписку",
					CallbackData: "claim_bonus",
				},
			},
		},
	}

	err := h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   "HTML",
		ReplyMarkup: keyboard,
	})

	if err != nil {
		h.logger.Error("Failed to send bonus message",
			zap.Int64("chat_id", chatID),
			zap.Error(err))
	}
}

// bcCallbackPrefixApprove / bcCallbackPrefixCancel — callback_data префиксы
// для retention-broadcast inline-кнопок (см. service/retention_cron.go в
// auth-service). Формат: bc_approve_<id> / bc_cancel_<id>.
const (
	bcCallbackPrefixApprove = "bc_approve_"
	bcCallbackPrefixCancel  = "bc_cancel_"
)

// handleCallback обрабатывает callback'и от inline кнопок.
//
// Каждый callback требует AnswerCallbackQuery (иначе у юзера крутится
// loader). Конкретные хендлеры сами вызывают answerCallback — так можно
// показывать alert/text. Если хендлер не найден — отвечаем пустым.
func (h *TelegramBotHandler) handleCallback(ctx context.Context, callback *CallbackQuery) {
	switch {
	case callback.Data == "claim_bonus":
		h.handleClaimBonus(ctx, callback)
	case strings.HasPrefix(callback.Data, bcCallbackPrefixApprove):
		h.handleBroadcastApprove(ctx, callback,
			strings.TrimPrefix(callback.Data, bcCallbackPrefixApprove))
	case strings.HasPrefix(callback.Data, bcCallbackPrefixCancel):
		h.handleBroadcastCancel(ctx, callback,
			strings.TrimPrefix(callback.Data, bcCallbackPrefixCancel))
	default:
		// Неизвестный callback — закрываем loader, чтобы не висел.
		h.answerCallback(ctx, callback.ID, "", false)
	}
}

// handleClaimBonus обрабатывает начисление бонуса
func (h *TelegramBotHandler) handleClaimBonus(ctx context.Context, callback *CallbackQuery) {
	userID := callback.From.ID

	// 1. Проверяем подписку на канал
	member, err := h.telegramClient.GetChatMember(ctx, telegram.GetChatMemberParams{
		ChatID: h.channelUsername,
		UserID: userID,
	})
	if err != nil {
		h.logger.Error("Failed to check channel subscription",
			zap.Int64("user_id", userID),
			zap.Error(err))
		h.answerCallback(ctx, callback.ID, "❌ Ошибка проверки подписки", true)
		return
	}

	// Проверяем статус
	if member.Status != "creator" && member.Status != "administrator" && member.Status != "member" {
		h.answerCallback(ctx, callback.ID, "❌ Сначала подпишитесь на канал!", true)
		return
	}

	// 2. Начисляем бонус через Subscription Service
	resp, err := h.subscriptionClient.ClaimChannelBonus(ctx, &pb.ClaimChannelBonusRequest{
		UserId: userID,
	})
	if err != nil {
		h.logger.Error("Failed to claim channel bonus",
			zap.Int64("user_id", userID),
			zap.Error(err))
		h.answerCallback(ctx, callback.ID, "❌ Ошибка начисления бонуса", true)
		return
	}

	// 3. Формируем ответ
	var responseText string
	if resp.AlreadyClaimed {
		responseText = "ℹ️ Вы уже получили этот бонус ранее"
	} else if resp.NoActiveSubscription {
		// Получаем информацию о подписке для отображения
		subInfo, err := h.subscriptionClient.GetActiveSubscription(ctx, userID)
		if err != nil || !subInfo.HasActive || subInfo.Subscription == nil {
			responseText = "❌ Бонус можно получить только при активной подписке\n\n💡 Откройте Mini App и активируйте подписку, чтобы получить +3 дня в подарок!"
		} else {
			sub := subInfo.Subscription
			statusText := "подписка"
			if sub.Status == "trial" {
				statusText = "пробная версия"
			}
			// Рассчитываем оставшиеся дни
			expiresAt, _ := time.Parse(time.RFC3339, sub.ExpiresAt)
			daysLeft := int(time.Until(expiresAt).Hours() / 24)
			if daysLeft < 0 {
				daysLeft = 0
			}
			responseText = fmt.Sprintf("❌ Бонус можно получить только при активной подписке\n\n📅 У вас сейчас: %s\n⏰ Осталось: %d дн.\n\n💡 Откройте Mini App для активации", statusText, daysLeft)
		}
	} else if resp.Success {
		expiresAt := resp.Subscription.ExpiresAt
		responseText = fmt.Sprintf("✅ Бонус начислен!\n\n+3 дня к подписке\nНовая дата окончания: %s", expiresAt)
	} else {
		responseText = "❌ Не удалось начислить бонус"
	}

	// 4. Обновляем сообщение
	h.editMessage(ctx, callback.Message.Chat.ID, callback.Message.MessageID, responseText)
	h.answerCallback(ctx, callback.ID, "", false)
}

// answerCallback отправляет ответ на callback query
func (h *TelegramBotHandler) answerCallback(ctx context.Context, callbackID, text string, showAlert bool) {
	_ = h.telegramClient.AnswerCallbackQuery(ctx, telegram.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
		Text:            text,
		ShowAlert:       showAlert,
	})
}

// handleBroadcastApprove — обработчик callback'а "✅ Approve #N" под превью
// retention-рассылки. Вызывает auth-service BroadcastService.ApproveBroadcast,
// который проверяет admin-роль и стартует sender в фоне.
//
// idStr — суффикс после "bc_approve_". Парсим в int64.
func (h *TelegramBotHandler) handleBroadcastApprove(ctx context.Context, callback *CallbackQuery, idStr string) {
	if h.broadcastClient == nil {
		h.answerCallback(ctx, callback.ID, "❌ Broadcast-клиент не настроен", true)
		return
	}
	draftID, err := parseInt64(idStr)
	if err != nil || draftID <= 0 {
		h.answerCallback(ctx, callback.ID, "❌ Некорректный ID draft'а", true)
		return
	}

	resp, err := h.broadcastClient.ApproveBroadcast(ctx, draftID, callback.From.ID)
	if err != nil {
		h.logger.Warn("broadcast approve failed",
			zap.Int64("draft_id", draftID),
			zap.Int64("admin_tg_id", callback.From.ID),
			zap.Error(err),
		)
		h.answerCallback(ctx, callback.ID,
			fmt.Sprintf("❌ Ошибка: %s", briefGRPCError(err)), true)
		return
	}

	h.logger.Info("broadcast approved",
		zap.Int64("draft_id", draftID),
		zap.Int64("admin_tg_id", callback.From.ID),
		zap.Int32("recipients", resp.RecipientCount),
	)

	// Заменяем preview-message на статусную плашку, чтобы кнопки исчезли.
	if callback.Message != nil {
		newText := fmt.Sprintf("✅ Approved (#%d) — рассылка %d юзерам запущена.\n"+
			"Итог придёт отдельным сообщением через несколько секунд.",
			draftID, resp.RecipientCount)
		h.editMessage(ctx, callback.Message.Chat.ID, callback.Message.MessageID, newText)
	}
	h.answerCallback(ctx, callback.ID, "✅ Запущено", false)
}

// handleBroadcastCancel — обработчик "❌ Cancel #N". Меняет status='cancelled'.
func (h *TelegramBotHandler) handleBroadcastCancel(ctx context.Context, callback *CallbackQuery, idStr string) {
	if h.broadcastClient == nil {
		h.answerCallback(ctx, callback.ID, "❌ Broadcast-клиент не настроен", true)
		return
	}
	draftID, err := parseInt64(idStr)
	if err != nil || draftID <= 0 {
		h.answerCallback(ctx, callback.ID, "❌ Некорректный ID draft'а", true)
		return
	}

	_, err = h.broadcastClient.CancelBroadcast(ctx, draftID, callback.From.ID)
	if err != nil {
		h.logger.Warn("broadcast cancel failed",
			zap.Int64("draft_id", draftID),
			zap.Int64("admin_tg_id", callback.From.ID),
			zap.Error(err),
		)
		h.answerCallback(ctx, callback.ID,
			fmt.Sprintf("❌ Ошибка: %s", briefGRPCError(err)), true)
		return
	}

	h.logger.Info("broadcast cancelled",
		zap.Int64("draft_id", draftID),
		zap.Int64("admin_tg_id", callback.From.ID),
	)

	if callback.Message != nil {
		newText := fmt.Sprintf("❌ Cancelled (#%d) — рассылка отменена.", draftID)
		h.editMessage(ctx, callback.Message.Chat.ID, callback.Message.MessageID, newText)
	}
	h.answerCallback(ctx, callback.ID, "Отменено", false)
}

// parseInt64 — strconv.ParseInt одной строкой; завёрнут чтобы не ломать
// импорт-блок strconv (в этом файле его пока нет, и это единственное
// место где он понадобился).
func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// briefGRPCError — короткое описание для Telegram alert (без RPC-внутренностей).
// gRPC ошибки вида "rpc error: code = PermissionDenied desc = admin role required"
// человеку показывать не стоит — режем до desc'а.
func briefGRPCError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.Index(msg, "desc = "); i != -1 {
		return msg[i+len("desc = "):]
	}
	return msg
}

// editMessage редактирует текст сообщения
func (h *TelegramBotHandler) editMessage(ctx context.Context, chatID, messageID int64, text string) {
	err := h.telegramClient.EditMessageText(ctx, telegram.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      text,
		ParseMode: "HTML",
	})
	if err != nil {
		h.logger.Error("Failed to edit message",
			zap.Int64("chat_id", chatID),
			zap.Int64("message_id", messageID),
			zap.Error(err))
	}
}
