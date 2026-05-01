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

// TelegramBotHandler обрабатывает команды и callback'и от Telegram бота
type TelegramBotHandler struct {
	telegramClient     *telegram.Client
	subscriptionClient *client.SubscriptionClient
	authClient         *client.AuthClient
	logger             *zap.Logger
	channelUsername    string
}

func NewTelegramBotHandler(
	telegramClient *telegram.Client,
	subscriptionClient *client.SubscriptionClient,
	authClient *client.AuthClient,
	logger *zap.Logger,
	channelUsername string,
) *TelegramBotHandler {
	return &TelegramBotHandler{
		telegramClient:     telegramClient,
		subscriptionClient: subscriptionClient,
		authClient:         authClient,
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
		h.handleStart(ctx, msg.Chat.ID, msg.From.ID, param)
	case "/bonus", "/bonus@maydavpnbot":
		h.sendBonusMessage(ctx, msg.Chat.ID)
	}
}

// handleStart обрабатывает /start [param]. Если param == "ref_<token>" —
// сохраняем атрибуцию через auth-service, чтобы при первом ValidateTelegramUser
// (когда юзер откроет Mini App) рефералка зарегистрировалась.
func (h *TelegramBotHandler) handleStart(ctx context.Context, chatID, telegramUserID int64, startParam string) {
	if strings.HasPrefix(startParam, refStartParamPrefix) {
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

// handleCallback обрабатывает callback'и от inline кнопок
func (h *TelegramBotHandler) handleCallback(ctx context.Context, callback *CallbackQuery) {
	// Сначала отвечаем на callback (убираем loader)
	defer func() {
		_ = h.telegramClient.AnswerCallbackQuery(ctx, telegram.AnswerCallbackQueryParams{
			CallbackQueryID: callback.ID,
		})
	}()

	switch callback.Data {
	case "claim_bonus":
		h.handleClaimBonus(ctx, callback)
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
