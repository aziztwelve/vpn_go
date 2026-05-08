package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vpn/gateway/internal/client"
	"github.com/vpn/platform/pkg/telegram"
	broadcastpb "github.com/vpn/shared/pkg/proto/broadcast/v1"
	pb "github.com/vpn/shared/pkg/proto/subscription/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Префиксы start-параметра deep-link'ов:
//
//	ref_<token>  — реферальная ссылка (https://t.me/<bot>?start=ref_<token>)
//	src_<slug>   — маркетинговая воронка/кампания
//
// Парсинг сейчас делается в auth-service.RegisterFromBot (см. RefStartPrefix /
// CampaignSrcStartPrefix там же); префиксы здесь оставлены документацией
// для читателя. Гейтвей их теперь сам не парсит — просто проксирует start_param.

// TelegramBotHandler обрабатывает команды и callback'и от Telegram бота.
// broadcastClient может быть nil — тогда retention-callback'и (bc_*) будут
// игнорироваться (полезно для dev окружений без auth-service'а).
// promoClient — для админ-команды /promo @username (см. handlePromo).
// paymentClient/vpnClient — зарезервированы для будущих бот-команд (например
// /pay для прямой оплаты из бота, /servers для списка локаций); пока ими
// никто из bot-handler'ов не пользуется, но клиенты прокидываются из app.go,
// чтобы при добавлении команды не пришлось трогать DI-граф.
type TelegramBotHandler struct {
	telegramClient     *telegram.Client
	subscriptionClient *client.SubscriptionClient
	authClient         *client.AuthClient
	broadcastClient    *client.BroadcastClient
	promoClient        *client.PromoClient
	paymentClient      *client.PaymentClient
	vpnClient          *client.VPNClient
	logger             *zap.Logger
	channelUsername    string
}

func NewTelegramBotHandler(
	telegramClient *telegram.Client,
	subscriptionClient *client.SubscriptionClient,
	authClient *client.AuthClient,
	broadcastClient *client.BroadcastClient,
	promoClient *client.PromoClient,
	paymentClient *client.PaymentClient,
	vpnClient *client.VPNClient,
	logger *zap.Logger,
	channelUsername string,
) *TelegramBotHandler {
	return &TelegramBotHandler{
		telegramClient:     telegramClient,
		subscriptionClient: subscriptionClient,
		authClient:         authClient,
		broadcastClient:    broadcastClient,
		promoClient:        promoClient,
		paymentClient:      paymentClient,
		vpnClient:          vpnClient,
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
//
// Reply-keyboard (постоянная клавиатура внизу чата, см. task 18):
// тексты кнопок Telegram отправляет ботy как обычные сообщения, поэтому
// мы матчим их text == replyBtnConnect/replyBtnBuy ДО разбора /команд
// (символы reply-кнопок начинаются с эмодзи, конфликта со slash-командами нет).
//
// Admin-команды (видимые только для users.role='admin', не-админам
// auth-service возвращает PermissionDenied и бот молча игнорит):
//
//	/admin                       — список pending broadcast-драфтов
//	/approve_<id>                — approve draft, запуск sender'а
//	/cancel_<id>                 — отмена draft (status='draft')
//	/broadcast_stats <id>        — статы доставки (sent/blocked/...)
//	/broadcast_stats_<id>        — то же, в underscore-форме (clickable)
func (h *TelegramBotHandler) handleCommand(ctx context.Context, msg *Message) {
	text := strings.TrimSpace(msg.Text)

	// 1. Match reply-keyboard text-кнопок (приходят как обычный текст). Делаем
	//    раньше парсинга /команд, чтобы не зависеть от наличия "/" префикса.
	switch text {
	case replyBtnConnect:
		h.handleConnectButton(ctx, msg.Chat.ID, msg.From.ID)
		return
	case replyBtnBuy:
		h.handleBuyButton(ctx, msg.Chat.ID, msg.From.ID)
		return
	}

	// Разбираем "/start" или "/start@maydavpnbot" с опциональным параметром.
	cmd, param, _ := strings.Cut(text, " ")
	cmd = strings.TrimSpace(cmd)
	param = strings.TrimSpace(param)

	// В групповых чатах команды могут приходить с "@maydavpnbot"-суффиксом —
	// отрезаем перед матчингом, чтобы /approve_42@maydavpnbot тоже работал.
	if i := strings.Index(cmd, "@"); i != -1 {
		cmd = cmd[:i]
	}

	switch {
	case cmd == "/start":
		h.handleStart(ctx, msg.Chat.ID, msg.From.ID, param, msg.From)
	case cmd == "/bonus":
		h.sendBonusMessage(ctx, msg.Chat.ID)
	case cmd == "/admin":
		h.handleAdminList(ctx, msg.Chat.ID, msg.From.ID)
	case strings.HasPrefix(cmd, "/approve_"):
		h.handleAdminApprove(ctx, msg.Chat.ID, msg.From.ID,
			strings.TrimPrefix(cmd, "/approve_"))
	case strings.HasPrefix(cmd, "/cancel_"):
		h.handleAdminCancel(ctx, msg.Chat.ID, msg.From.ID,
			strings.TrimPrefix(cmd, "/cancel_"))
	case cmd == "/broadcast_stats":
		h.handleAdminStats(ctx, msg.Chat.ID, msg.From.ID, param)
	case strings.HasPrefix(cmd, "/broadcast_stats_"):
		h.handleAdminStats(ctx, msg.Chat.ID, msg.From.ID,
			strings.TrimPrefix(cmd, "/broadcast_stats_"))
	case cmd == "/promo":
		h.handlePromo(ctx, msg.Chat.ID, msg.From.ID, param)
	case cmd == "/promo_status":
		h.handlePromoStatus(ctx, msg.Chat.ID, msg.From.ID, param)
	}
}

// handleStart обрабатывает /start [param]. Поддерживаемые параметры:
//
//	ref_<token> — реферальная ссылка (персональная от юзера)
//	src_<slug>  — маркетинговая воронка/кампания (от админа для блогера)
//
// При /start полностью инициализируем юзера (как раньше при первом открытии
// Mini App) — чтобы он мог сразу пользоваться VPN из бота, без необходимости
// открывать приложение:
//
//   1. RecordBotStart — фиксируем нажатие в bot_starts (воронка).
//   2. RegisterFromBot — upsert users + синхронная атрибуция ref_/src_
//      (заменяет старый pending-flow + ValidateTelegramUser-обработку).
//   3. Если юзер только что создан — StartTrial (3 дня) + CreateVPNUser
//      (UUID в Xray). Этот шаг идентичен activateTrial в HTTP-handler'е,
//      просто переехал на момент /start.
//   4. sendStartMessage (welcome + inline) + sendPostStartLinkOrCTA
//      (VLESS-ссылка либо CTA «купи подписку», с reply-keyboard внизу).
//
// Любая ошибка на шагах 2-3 не валит UX: юзер всё равно получит welcome,
// и activateTrial в ValidateTelegramUser добьёт инициализацию когда юзер
// откроет Mini App (idempotent через WasAlreadyUsed/CreateVPNUser).
// При сбое регистрации userID=0 → sendPostStartLinkOrCTA отдаст CTA-фолбек
// и всё равно повесит reply-keyboard (юзер сможет нажать «🌐 Подключиться»
// внизу позже).
//
// Telegram update не содержит last_name/photo_url, передаём только то что есть.
func (h *TelegramBotHandler) handleStart(ctx context.Context, chatID, telegramUserID int64, startParam string, from *User) {
	username, firstName := "", ""
	if from != nil {
		username = from.Username
		firstName = from.FirstName
	}

	// Шаг 1: воронка. auth-service сам резолвит src_<slug> в campaign_id
	// при записи в bot_starts. Best-effort.
	if _, _, err := h.authClient.RecordBotStart(ctx, telegramUserID, username, firstName, startParam); err != nil {
		h.logger.Warn("Failed to record bot start",
			zap.Int64("telegram_id", telegramUserID),
			zap.Error(err))
	}

	// Шаг 2: полная регистрация юзера + ref/campaign атрибуция. Параметры
	// last_name/language_code не приходят в bot update'ах — передаём пусто
	// (auth-service оставит существующие значения для уже зарегистрированного
	// юзера; для нового юзера допустим пустой language_code, обновится при
	// открытии Mini App из initData).
	regResp, err := h.authClient.RegisterFromBot(
		ctx, telegramUserID, username, firstName, "", "", startParam,
	)
	if err != nil {
		// Регистрация упала — продолжаем UX-flow, отправляем welcome без
		// trial. ValidateTelegramUser в Mini App довершит инициализацию.
		// userID=0 → sendPostStartLinkOrCTA отдаст CTA-фолбек, но всё равно
		// поставит reply-keyboard юзеру.
		h.logger.Error("RegisterFromBot failed",
			zap.Int64("telegram_id", telegramUserID),
			zap.String("start_param", startParam),
			zap.Error(err))
		h.sendStartMessage(ctx, chatID, telegramUserID)
		h.sendPostStartLinkOrCTA(ctx, chatID, 0, telegramUserID)
		return
	}
	h.logger.Info("user registered via /start",
		zap.Int64("telegram_id", telegramUserID),
		zap.Int64("user_id", regResp.User.Id),
		zap.Bool("is_new_user", regResp.IsNewUser),
		zap.Bool("referral_registered", regResp.ReferralRegistered),
		zap.Int64("attributed_campaign_id", regResp.AttributedCampaignId),
		zap.String("start_param", startParam),
	)

	// Шаг 3: для новых юзеров активируем trial + регистрируем UUID в Xray.
	// Best-effort: если хоть что-то упало — пишем warn и отдаём welcome.
	// Юзер потом может довести инициализацию открытием Mini App.
	if regResp.IsNewUser {
		h.activateTrialFromBot(ctx, telegramUserID, regResp.User.Id)
	}

	// Шаг 4: welcome (inline-кнопки) + 2-е сообщение с VLESS-ссылкой или
	// CTA-фолбеком, в обоих случаях с reply-keyboard внизу чата.
	h.sendStartMessage(ctx, chatID, telegramUserID)
	h.sendPostStartLinkOrCTA(ctx, chatID, regResp.User.Id, telegramUserID)
}

// activateTrialFromBot — копия activateTrial из AuthHandler для бот-flow.
// Дёргает StartTrial (subscription-service) и CreateVPNUser (vpn-service).
// Best-effort: ошибки логируем, но не возвращаем — UX от этого не зависит.
func (h *TelegramBotHandler) activateTrialFromBot(ctx context.Context, telegramID, userID int64) {
	if h.subscriptionClient == nil || h.vpnClient == nil {
		h.logger.Warn("trial activation skipped: sub/vpn client not configured",
			zap.Int64("user_id", userID))
		return
	}

	trialResp, err := h.subscriptionClient.StartTrial(ctx, userID)
	if err != nil {
		h.logger.Error("StartTrial failed in /start flow",
			zap.Int64("tg_id", telegramID),
			zap.Int64("user_id", userID), zap.Error(err))
		return
	}
	if trialResp.WasAlreadyUsed {
		// Edge-case: auth-service сказал isNewUser=true (юзера в users не было),
		// но в subscription-service trial_used_at уже стоит. Возможно при
		// ручном удалении users или race. Не дублируем подписку.
		h.logger.Warn("trial was already used for fresh /start user",
			zap.Int64("tg_id", telegramID), zap.Int64("user_id", userID))
		return
	}

	if trialResp.Subscription == nil {
		h.logger.Warn("StartTrial returned nil subscription",
			zap.Int64("tg_id", telegramID), zap.Int64("user_id", userID))
		return
	}

	if _, err := h.vpnClient.CreateVPNUser(ctx, userID, trialResp.Subscription.Id); err != nil {
		h.logger.Error("CreateVPNUser failed after trial in /start flow",
			zap.Int64("tg_id", telegramID),
			zap.Int64("user_id", userID),
			zap.Int64("subscription_id", trialResp.Subscription.Id),
			zap.Error(err))
		// Подписка в БД есть, VPN-юзер не создан. Юзер заведётся при
		// первом GetSubscriptionToken / при открытии Mini App.
		return
	}

	h.logger.Info("trial activated from /start",
		zap.Int64("tg_id", telegramID),
		zap.Int64("user_id", userID),
		zap.Int64("subscription_id", trialResp.Subscription.Id),
	)
}

// sendStartMessage отправляет приветственное сообщение с кнопкой открытия Mini App
func (h *TelegramBotHandler) sendStartMessage(ctx context.Context, chatID int64, userID int64) {
	text := `👋 <b>Добро пожаловать в MaydaVPN!</b>

🔒 Быстрый и безопасный VPN для вашей конфиденциальности

✨ Вы получили <b>3 дня пробного периода</b>

Подпишитесь на канал @maydavpn
Техподдержка — @maydavpn_support

Жми 👇🏻`

	// Welcome-клавиатура: full-width "🚀 Открыть приложение" сверху + ряд из
	// "🌐 Подключиться" / "🛒 Купить подписку" под ним. Последние две дублируют
	// reply-кнопки внизу чата (см. sendReplyKeyboard) — это сделано чтобы юзер
	// мог сразу из welcome'а открыть карточку статуса или тарифы, не свайпая
	// в reply-кнопки. Callback'и cbConnectPrompt/cbBuyPrompt в handleCallback
	// проксируются на handleConnectButton/handleBuyButton.
	keyboard := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{
				{
					Text:   "🚀 Открыть приложение",
					WebApp: &telegram.WebAppInfo{URL: webAppRootURL},
				},
			},
			{
				{Text: replyBtnConnect, CallbackData: cbConnectPrompt},
				{Text: replyBtnBuy, CallbackData: cbBuyPrompt},
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

	// Раньше тут отправлялось 2-е сообщение «👇 Выбери действие:» с
	// постоянной reply-keyboard. Убрано — те же действия теперь дублируются
	// inline-кнопками "🌐 Подключиться" / "🛒 Купить подписку" прямо в
	// welcome-клавиатуре выше. Text-match хендлеры в handleCommand на эти
	// строки оставлены — на случай если юзер пришлёт их текстом вручную.
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
	// Reply-keyboard flow (см. telegram_bot_buttons.go и task 18).
	case callback.Data == cbGetSubLink:
		h.handleGetSubLink(ctx, callback)
	case callback.Data == cbConnectPrompt:
		// Inline-аналог reply-кнопки "🌐 Подключиться" — продублирован в welcome,
		// чтобы юзер мог открыть карточку статуса прямо из стартового сообщения.
		h.answerCallback(ctx, callback.ID, "", false)
		if callback.Message != nil {
			h.handleConnectButton(ctx, callback.Message.Chat.ID, callback.From.ID)
		}
	case callback.Data == cbBuyPrompt:
		// Перенаправляем на тот же хендлер, что text-кнопка "Купить подписку".
		// Закрываем loader сразу — handleBuyButton сам шлёт новое сообщение.
		h.answerCallback(ctx, callback.ID, "", false)
		if callback.Message != nil {
			h.handleBuyButton(ctx, callback.Message.Chat.ID, callback.From.ID)
		}
	case strings.HasPrefix(callback.Data, cbBuyQuickPrefix):
		// DEPRECATED: переадресует на новый buy_confirm-flow.
		h.handleBuyQuick(ctx, callback,
			strings.TrimPrefix(callback.Data, cbBuyQuickPrefix))
	case strings.HasPrefix(callback.Data, cbBuyPlanPrefix):
		h.handleBuyPlan(ctx, callback,
			strings.TrimPrefix(callback.Data, cbBuyPlanPrefix))
	case strings.HasPrefix(callback.Data, cbBuyConfirmPrefix):
		// "<plan_id>_<max_devices>" — парсим обе части.
		parts := strings.SplitN(strings.TrimPrefix(callback.Data, cbBuyConfirmPrefix), "_", 2)
		if len(parts) != 2 {
			h.answerCallback(ctx, callback.ID, "❌ Некорректные параметры", true)
			break
		}
		planID64, err1 := strconv.ParseInt(parts[0], 10, 32)
		dev64, err2 := strconv.ParseInt(parts[1], 10, 32)
		if err1 != nil || err2 != nil || planID64 <= 0 || dev64 <= 0 {
			h.answerCallback(ctx, callback.ID, "❌ Некорректные параметры", true)
			break
		}
		h.handleBuyConfirm(ctx, callback, int32(planID64), int32(dev64))
	case callback.Data == cbBuyBackPlans:
		h.handleBuyBackToPlans(ctx, callback)
	case strings.HasPrefix(callback.Data, cbBuyBackDevicesPrefix):
		// «◀️ Назад» с invoice → re-render device picker для того же plan_id.
		h.handleBuyPlan(ctx, callback,
			strings.TrimPrefix(callback.Data, cbBuyBackDevicesPrefix))
	case callback.Data == cbCancelInvoice:
		h.handleCancelInvoice(ctx, callback)
	default:
		// Неизвестный callback — закрываем loader, чтобы не висел.
		h.answerCallback(ctx, callback.ID, "", false)
	}
}

// handleClaimBonus обрабатывает начисление бонуса.
//
// Важно: callback.From.ID — это telegram_id, а subscription-service ждёт
// внутренний users.id (см. ClaimChannelBonusTx / GetActiveSubscription —
// оба джойнят по subscriptions.user_id == users.id). До добавления
// GetUserByTelegramID этот хендлер передавал telegram_id напрямую как
// user_id, из-за чего бонус никогда не попадал на нужную подписку у юзеров,
// у которых users.id != telegram_id (т.е. у всех, кроме первых регистраций).
//
// Шаги:
//  1. Резолвим telegram_id → users.id через AuthClient.GetUserByTelegramID.
//     NotFound = юзер не открыл Mini App ни разу — бонус физически некуда
//     начислять, мягко просим зайти.
//  2. Проверяем подписку на канал (с telegram_id, как требует Bot API).
//  3. Дёргаем ClaimChannelBonus / GetActiveSubscription уже с users.id.
func (h *TelegramBotHandler) handleClaimBonus(ctx context.Context, callback *CallbackQuery) {
	tgID := callback.From.ID

	// 1. Маппинг telegram_id → users.id.
	userResp, err := h.authClient.GetUserByTelegramID(ctx, tgID)
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			h.logger.Info("claim_bonus: user not registered yet",
				zap.Int64("tg_id", tgID))
			h.answerCallback(ctx, callback.ID,
				"❌ Сначала откройте Mini App, чтобы активировать аккаунт", true)
			return
		}
		h.logger.Error("claim_bonus: failed to resolve user by telegram_id",
			zap.Int64("tg_id", tgID), zap.Error(err))
		h.answerCallback(ctx, callback.ID, "❌ Ошибка проверки аккаунта", true)
		return
	}
	userID := userResp.User.Id

	// 2. Проверяем подписку на канал. Bot API оперирует telegram_id, поэтому
	// тут используем именно tgID, а не users.id.
	member, err := h.telegramClient.GetChatMember(ctx, telegram.GetChatMemberParams{
		ChatID: h.channelUsername,
		UserID: tgID,
	})
	if err != nil {
		h.logger.Error("Failed to check channel subscription",
			zap.Int64("tg_id", tgID),
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

	// 3. Начисляем бонус через Subscription Service (передаём users.id).
	resp, err := h.subscriptionClient.ClaimChannelBonus(ctx, &pb.ClaimChannelBonusRequest{
		UserId: userID,
	})
	if err != nil {
		h.logger.Error("Failed to claim channel bonus",
			zap.Int64("tg_id", tgID),
			zap.Int64("user_id", userID),
			zap.Error(err))
		h.answerCallback(ctx, callback.ID, "❌ Ошибка начисления бонуса", true)
		return
	}

	// 4. Формируем ответ
	var responseText string
	if resp.AlreadyClaimed {
		responseText = "ℹ️ Вы уже получили этот бонус ранее"
	} else if resp.NoActiveSubscription {
		// Получаем информацию о подписке для отображения (тоже по users.id).
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

	resp, err := h.broadcastClient.ApproveBroadcastByTelegramID(ctx, draftID, callback.From.ID)
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

	_, err = h.broadcastClient.CancelBroadcastByTelegramID(ctx, draftID, callback.From.ID)
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

// ─── Admin commands (Stage 5) ──────────────────────────────────────
//
// Дублируют функционал inline-callback'ов (bc_approve_/bc_cancel_) и
// HTTP-админки. Полезны когда:
//   - оригинальное notify-сообщение от RetentionCron потерялось/удалено;
//   - админ хочет посмотреть статус прошлой рассылки без открытия curl;
//   - быстрый approve по ID из логов/чужого сообщения.
//
// Авторизация: BroadcastService.authorize() сверяет
// callback.From.ID с users.role='admin'. Не-админы получат
// PermissionDenied → бот отвечает молчанием (через grpcSilentForNonAdmin).
//
// Все ответы — обычные SendMessage в чат с ботом, без inline-кнопок,
// чтобы не плодить ещё больше путей подтверждения.

// handleAdminList — /admin: показывает pending драфты со списком команд
// для каждого. Если pending нет — отдаёт сводку по последним 5
// рассылкам (чтобы команда не выглядела сломанной).
func (h *TelegramBotHandler) handleAdminList(ctx context.Context, chatID, tgID int64) {
	if h.broadcastClient == nil {
		return
	}

	resp, err := h.broadcastClient.ListBroadcastsByTelegramID(ctx, tgID, "draft", "", 50, 0)
	if err != nil {
		if h.grpcSilentForNonAdmin(ctx, chatID, tgID, "admin list", err) {
			return
		}
		h.sendPlainMessage(ctx, chatID, fmt.Sprintf("❌ Ошибка: %s", briefGRPCError(err)))
		return
	}

	if len(resp.Items) == 0 {
		// Pending'ов нет — покажем последние 5 любых, чтобы админ
		// убедился что система жива и мог посмотреть статы.
		recent, rerr := h.broadcastClient.ListBroadcastsByTelegramID(ctx, tgID, "", "", 5, 0)
		if rerr != nil || len(recent.Items) == 0 {
			h.sendPlainMessage(ctx, chatID, "🛠 Admin\n\n📭 Нет pending драфтов и ни одной рассылки.")
			return
		}
		h.sendPlainMessage(ctx, chatID, formatAdminList(nil, recent.Items))
		return
	}

	// Дополнительно подгрузим последние 5 любого статуса для контекста.
	recent, _ := h.broadcastClient.ListBroadcastsByTelegramID(ctx, tgID, "", "", 5, 0)
	var recentItems []*broadcastpb.DraftSummary
	if recent != nil {
		recentItems = recent.Items
	}
	h.sendPlainMessage(ctx, chatID, formatAdminList(resp.Items, recentItems))
}

// handleAdminApprove — /approve_<id>: дублирует bc_approve_<id> callback.
func (h *TelegramBotHandler) handleAdminApprove(ctx context.Context, chatID, tgID int64, idStr string) {
	if h.broadcastClient == nil {
		return
	}
	draftID, err := parseInt64(idStr)
	if err != nil || draftID <= 0 {
		h.sendPlainMessage(ctx, chatID, "❌ Некорректный ID.\nПример: /approve_42")
		return
	}

	resp, err := h.broadcastClient.ApproveBroadcastByTelegramID(ctx, draftID, tgID)
	if err != nil {
		if h.grpcSilentForNonAdmin(ctx, chatID, tgID, "approve", err) {
			return
		}
		h.logger.Warn("bot /approve failed",
			zap.Int64("draft_id", draftID), zap.Int64("admin_tg_id", tgID), zap.Error(err))
		h.sendPlainMessage(ctx, chatID,
			fmt.Sprintf("❌ Ошибка approve #%d: %s", draftID, briefGRPCError(err)))
		return
	}

	h.logger.Info("bot /approve",
		zap.Int64("draft_id", draftID),
		zap.Int64("admin_tg_id", tgID),
		zap.Int32("recipients", resp.RecipientCount),
	)
	h.sendPlainMessage(ctx, chatID, fmt.Sprintf(
		"✅ Approved #%d — рассылка %d юзерам запущена.\n"+
			"Итог придёт через несколько секунд.\n"+
			"Прогресс: /broadcast_stats_%d",
		draftID, resp.RecipientCount, draftID))
}

// handleAdminCancel — /cancel_<id>: дублирует bc_cancel_<id> callback.
func (h *TelegramBotHandler) handleAdminCancel(ctx context.Context, chatID, tgID int64, idStr string) {
	if h.broadcastClient == nil {
		return
	}
	draftID, err := parseInt64(idStr)
	if err != nil || draftID <= 0 {
		h.sendPlainMessage(ctx, chatID, "❌ Некорректный ID.\nПример: /cancel_42")
		return
	}

	if _, err := h.broadcastClient.CancelBroadcastByTelegramID(ctx, draftID, tgID); err != nil {
		if h.grpcSilentForNonAdmin(ctx, chatID, tgID, "cancel", err) {
			return
		}
		h.logger.Warn("bot /cancel failed",
			zap.Int64("draft_id", draftID), zap.Int64("admin_tg_id", tgID), zap.Error(err))
		h.sendPlainMessage(ctx, chatID,
			fmt.Sprintf("❌ Ошибка cancel #%d: %s", draftID, briefGRPCError(err)))
		return
	}

	h.logger.Info("bot /cancel",
		zap.Int64("draft_id", draftID), zap.Int64("admin_tg_id", tgID))
	h.sendPlainMessage(ctx, chatID, fmt.Sprintf("❌ Cancelled #%d.", draftID))
}

// handleAdminStats — /broadcast_stats <id>: показ статов доставки.
func (h *TelegramBotHandler) handleAdminStats(ctx context.Context, chatID, tgID int64, idStr string) {
	if h.broadcastClient == nil {
		return
	}
	if idStr == "" {
		h.sendPlainMessage(ctx, chatID,
			"❌ Укажите ID.\nПример: /broadcast_stats 42 или /broadcast_stats_42")
		return
	}
	draftID, err := parseInt64(idStr)
	if err != nil || draftID <= 0 {
		h.sendPlainMessage(ctx, chatID, "❌ Некорректный ID.")
		return
	}

	d, err := h.broadcastClient.GetBroadcastDetailsByTelegramID(ctx, draftID, tgID)
	if err != nil {
		if h.grpcSilentForNonAdmin(ctx, chatID, tgID, "stats", err) {
			return
		}
		h.sendPlainMessage(ctx, chatID,
			fmt.Sprintf("❌ Не удалось получить статы #%d: %s", draftID, briefGRPCError(err)))
		return
	}
	h.sendPlainMessage(ctx, chatID, formatBroadcastDetails(d))
}

// grpcSilentForNonAdmin — если ошибка PermissionDenied, ничего не
// отправляем юзеру (не палим существование admin-команд) и логируем
// debug-уровнем. Возвращает true если ошибка обработана как
// "не-админ", иначе false (вызывающий сам решает что слать).
func (h *TelegramBotHandler) grpcSilentForNonAdmin(_ context.Context, chatID, tgID int64, op string, err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	if st.Code() == codes.PermissionDenied {
		h.logger.Debug("non-admin tried admin command",
			zap.String("op", op),
			zap.Int64("chat_id", chatID),
			zap.Int64("tg_id", tgID),
		)
		return true
	}
	return false
}

// sendPlainMessage — без HTML-парсинга (избегаем экранирования всех
// символов в шаблонах и сегмент-ключах). Без клавиатур.
func (h *TelegramBotHandler) sendPlainMessage(ctx context.Context, chatID int64, text string) {
	if err := h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	}); err != nil {
		h.logger.Error("Failed to send admin message",
			zap.Int64("chat_id", chatID), zap.Error(err))
	}
}

// formatAdminList — текст сообщения для /admin. pending — основной блок
// с командами; recent — компактная сводка, чтобы было видно что вообще
// происходило в системе.
func formatAdminList(pending, recent []*broadcastpb.DraftSummary) string {
	var b strings.Builder
	b.WriteString("🛠 Admin\n")

	if len(pending) > 0 {
		fmt.Fprintf(&b, "\n📋 Pending (%d):\n", len(pending))
		for _, d := range pending {
			fmt.Fprintf(&b, "\n#%d %s • %d получ. • %s\n   /approve_%d   /cancel_%d   /broadcast_stats_%d\n",
				d.Id, d.SegmentKey, d.RecipientCount, formatRelTime(d.CreatedAtUnix),
				d.Id, d.Id, d.Id)
		}
	} else {
		b.WriteString("\n📭 Нет pending драфтов.\n")
	}

	if len(recent) > 0 {
		b.WriteString("\nПоследние:\n")
		for _, d := range recent {
			fmt.Fprintf(&b, "#%d %s • %s • %d • %s\n",
				d.Id, statusEmoji(d.Status), d.SegmentKey, d.RecipientCount,
				formatRelTime(d.CreatedAtUnix))
		}
	}

	return b.String()
}

// formatBroadcastDetails — текст для /broadcast_stats <id>.
func formatBroadcastDetails(d *broadcastpb.BroadcastDetails) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 Broadcast #%d\n\n", d.Id)
	fmt.Fprintf(&b, "Status: %s %s\n", statusEmoji(d.Status), d.Status)
	fmt.Fprintf(&b, "Segment: %s\n", d.SegmentKey)
	fmt.Fprintf(&b, "Title: %s\n", d.Title)
	fmt.Fprintf(&b, "Recipients: %d\n", d.RecipientCount)
	fmt.Fprintf(&b, "Created: %s\n", formatRelTime(d.CreatedAtUnix))
	if d.ApprovedAtUnix > 0 {
		fmt.Fprintf(&b, "Approved: %s (admin user_id=%d)\n",
			formatRelTime(d.ApprovedAtUnix), d.ApprovedByUserId)
	}
	if d.SentAtUnix > 0 {
		fmt.Fprintf(&b, "Sent: %s\n", formatRelTime(d.SentAtUnix))
	}

	if d.Stats != nil {
		s := d.Stats
		b.WriteString("\n📈 Доставка:\n")
		fmt.Fprintf(&b, "✅ sent: %d\n", s.Sent)
		fmt.Fprintf(&b, "🚫 blocked: %d\n", s.Blocked)
		fmt.Fprintf(&b, "❌ failed: %d\n", s.Failed)
		fmt.Fprintf(&b, "👁 opened: %d\n", s.Opened)
		fmt.Fprintf(&b, "🖱 clicked: %d\n", s.Clicked)
	}

	if d.Status == "draft" {
		fmt.Fprintf(&b, "\nДействия:\n/approve_%d   /cancel_%d\n", d.Id, d.Id)
	}
	return b.String()
}

// statusEmoji — визуальный маркер для status'а в листингах.
func statusEmoji(s string) string {
	switch s {
	case "draft":
		return "📝"
	case "approved":
		return "✅"
	case "sending":
		return "📤"
	case "sent":
		return "📬"
	case "cancelled":
		return "🚫"
	case "failed":
		return "💥"
	default:
		return "❓"
	}
}

// formatRelTime — короткое относительное время, "5m ago" / "2h ago" /
// "3d ago" / "12.04". 0 → "—".
func formatRelTime(unix int64) string {
	if unix <= 0 {
		return "—"
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "только что"
	case d < time.Hour:
		return fmt.Sprintf("%dм назад", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dч назад", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dд назад", int(d.Hours()/24))
	default:
		return time.Unix(unix, 0).Format("02.01")
	}
}
