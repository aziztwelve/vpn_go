// Package handler — telegram_bot_promo.go: bot-команды для персональных
// промо-токенов (см. shared/proto/promo/v1/promo.proto + handler/promo.go).
//
// Команды:
//
//	/promo @username        — выдать промо-токен @username и сразу отправить
//	                          ему сообщение с кнопкой «💎 Оплатить 79₽».
//	/promo <telegram_id>    — то же, но цифрами (когда у юзера нет username).
//	/promo_status @username — посмотреть статус выданного промо.
//
// Plan-id и max_devices — захардкожены под план id=101 (Промо 79₽). При
// добавлении других акций — параметризовать через extra param `/promo @x 102`.
//
// Auth: PromoService.IssuePromo делает internal проверку role='admin' по
// telegram_id. Не-админы получают PermissionDenied, бот молчит (через
// grpcSilentForNonAdmin, как и /admin).
package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vpn/platform/pkg/telegram"
	promopb "github.com/vpn/shared/pkg/proto/promo/v1"
	"go.uber.org/zap"
)

// promoPlanID — id плана из subscription_plans. Если поменяется (например
// акция «199 → 99», новый план id=102), нужно поменять и здесь.
// Цена/название читаются из promoOfferText (статичный шаблон ниже).
const (
	promoPlanID     int32 = 101
	promoMaxDevices int32 = 2
)

// promoMiniAppBase — корневой URL фронта для составления promo-ссылки.
// Вместе с /promo/p/<token> формирует public CTA URL. Берётся из
// захардкоженного prod-домена (cdn.osmonai.com) — при появлении staging
// можно вынести в конфиг (Telegram.WebhookURL у Gateway).
const promoMiniAppBase = "https://cdn.osmonai.com"

// promoOfferText — шаблон сообщения которое получает юзер. Ключ {first_name}
// подставляется на месте через fmt.Sprintf (%s); знаки процента в тексте
// экранированы как %%.
const promoOfferText = `Ассалому алейкум, %s! 👋

Ваш пробный период MaydaVPN закончился.

🎁 Специально для вас — скидка 60%%:
   1 месяц VPN за 79₽ вместо 199₽

✅ Безлимитный трафик
✅ Без ограничений скорости
✅ Все ваши данные анонимны

Подключайтесь и пользуйтесь — наша команда старается для вас 💙

Скидка действует только для вашего аккаунта.
Нажмите кнопку ниже, чтобы оплатить.`

// promoSupportURL — кнопка «Поддержка» под основным CTA.
const promoSupportURL = "https://t.me/maydavpn_support"

// promoHowToURL — кнопка «Как подключиться»: ведёт в канал @maydavpn,
// там видео-инструкции по подключению клиентов (Happ, V2rayNG и т.п.).
const promoHowToURL = "https://t.me/maydavpn"

// handlePromo — /promo @username или /promo <telegram_id>.
//
// Шаги:
//  1. Парсим param — определяем @username/telegram_id.
//  2. PromoClient.LookupUser → user.id + telegram_id + first_name.
//  3. PromoClient.IssuePromo(user_id, plan_id=101, max_devices=2) → token.
//     Дубликат-выпуск идемпотентен через UNIQUE-индекс — alreadyExisted=true.
//  4. SendMessage юзеру с promoOfferText + inline button url=/promo/p/<token>.
//  5. Подтверждение админу: «✅ Промо отправлен @user, token=abc...».
func (h *TelegramBotHandler) handlePromo(ctx context.Context, chatID, adminTGID int64, param string) {
	if h.promoClient == nil {
		return
	}
	param = strings.TrimSpace(param)
	if param == "" {
		h.sendPlainMessage(ctx, chatID,
			"❌ Укажите получателя.\n"+
				"Примеры:\n"+
				"  /promo @aziztwelve\n"+
				"  /promo 123456789")
		return
	}

	// Шаг 1+2: LookupUser. Используем context с явным timeout — RPC к
	// auth-service быстрый, 5 сек хватает с большим запасом.
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	target, err := h.lookupPromoTarget(lookupCtx, adminTGID, param)
	if err != nil {
		if h.grpcSilentForNonAdmin(ctx, chatID, adminTGID, "promo lookup", err) {
			return
		}
		h.sendPlainMessage(ctx, chatID,
			fmt.Sprintf("❌ Не удалось найти юзера %q: %s", param, briefGRPCError(err)))
		return
	}

	// Шаг 3: IssuePromo. TTL=0 → дефолт 30 дней (см. DefaultPromoTTL).
	issueCtx, cancel2 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel2()

	issue, err := h.promoClient.IssuePromoByTelegramID(
		issueCtx, adminTGID, target.UserId,
		promoPlanID, promoMaxDevices, 0,
	)
	if err != nil {
		if h.grpcSilentForNonAdmin(ctx, chatID, adminTGID, "promo issue", err) {
			return
		}
		h.logger.Warn("promo: issue failed",
			zap.Int64("admin_tg_id", adminTGID),
			zap.Int64("target_user_id", target.UserId),
			zap.Error(err))
		h.sendPlainMessage(ctx, chatID,
			fmt.Sprintf("❌ Не удалось выпустить промо: %s", briefGRPCError(err)))
		return
	}

	// Шаг 4: отправляем юзеру сообщение с CTA.
	promoURL := fmt.Sprintf("%s/promo/p/%s", promoMiniAppBase, issue.Token)
	firstName := target.FirstName
	if firstName == "" {
		firstName = "друг"
	}
	userText := fmt.Sprintf(promoOfferText, firstName)
	// Layout:
	//   [💎 Оплатить 79₽]             ← главный CTA, full-width
	//   [📺 Как подключиться] [💬 Поддержка]  ← вспомогательные, 2 в ряд
	userKB := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{{Text: "💎 Оплатить 79₽", URL: promoURL}},
			{
				{Text: "📺 Как подключиться", URL: promoHowToURL},
				{Text: "💬 Поддержка", URL: promoSupportURL},
			},
		},
	}

	sendErr := h.telegramClient.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:                target.TelegramId,
		Text:                  userText,
		ReplyMarkup:           userKB,
		DisableWebPagePreview: true,
	})
	if sendErr != nil {
		// Юзер мог заблочить бота. В этом случае промо в БД уже создан,
		// но юзер сообщение не получит. Админ узнает что доставка не
		// прошла — сам решит что делать (пересылать вручную и т.п.).
		h.logger.Warn("promo: failed to deliver offer to user",
			zap.Int64("target_tg_id", target.TelegramId),
			zap.String("token_prefix", safePrefix(issue.Token)),
			zap.Error(sendErr))
		h.sendPlainMessage(ctx, chatID,
			h.formatPromoIssueAdmin(target, issue, promoURL, sendErr))
		return
	}

	h.logger.Info("promo: offer delivered",
		zap.Int64("admin_tg_id", adminTGID),
		zap.Int64("target_user_id", target.UserId),
		zap.Int64("target_tg_id", target.TelegramId),
		zap.String("token_prefix", safePrefix(issue.Token)),
		zap.Bool("already_existed", issue.AlreadyExisted),
	)

	h.sendPlainMessage(ctx, chatID,
		h.formatPromoIssueAdmin(target, issue, promoURL, nil))
}

// handlePromoStatus — /promo_status @username: показывает был ли выдан
// промо, активен ли он, использован ли.
func (h *TelegramBotHandler) handlePromoStatus(ctx context.Context, chatID, adminTGID int64, param string) {
	if h.promoClient == nil {
		return
	}
	param = strings.TrimSpace(param)
	if param == "" {
		h.sendPlainMessage(ctx, chatID,
			"❌ Укажите получателя.\nПример: /promo_status @aziztwelve")
		return
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	target, err := h.lookupPromoTarget(lookupCtx, adminTGID, param)
	if err != nil {
		if h.grpcSilentForNonAdmin(ctx, chatID, adminTGID, "promo lookup", err) {
			return
		}
		h.sendPlainMessage(ctx, chatID,
			fmt.Sprintf("❌ Не удалось найти юзера %q: %s", param, briefGRPCError(err)))
		return
	}

	statusCtx, cancel2 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel2()

	st, err := h.promoClient.GetPromoStatusByTelegramID(
		statusCtx, adminTGID, target.UserId, promoPlanID,
	)
	if err != nil {
		if h.grpcSilentForNonAdmin(ctx, chatID, adminTGID, "promo status", err) {
			return
		}
		h.sendPlainMessage(ctx, chatID,
			fmt.Sprintf("ℹ️ %s не имеет активного промо на план #%d.\n(%s)",
				promoTargetDisplay(target), promoPlanID, briefGRPCError(err)))
		return
	}

	h.sendPlainMessage(ctx, chatID, formatPromoStatusAdmin(target, st))
}

// lookupPromoTarget парсит param (@username или telegram_id) и резолвит
// в LookupUserResponse (содержит user_id, telegram_id, username, first_name).
func (h *TelegramBotHandler) lookupPromoTarget(
	ctx context.Context, adminTGID int64, param string,
) (*promopb.LookupUserResponse, error) {
	if strings.HasPrefix(param, "@") {
		uname := strings.TrimPrefix(param, "@")
		return h.promoClient.LookupUserByUsername(ctx, adminTGID, uname)
	}
	// Если param — число, пробуем как telegram_id.
	var tgID int64
	if _, scanErr := fmt.Sscanf(param, "%d", &tgID); scanErr == nil && tgID > 0 {
		return h.promoClient.LookupUserByTelegramID(ctx, adminTGID, tgID)
	}
	// Иначе пробуем как голый username (без @-префикса).
	return h.promoClient.LookupUserByUsername(ctx, adminTGID, param)
}

// formatPromoIssueAdmin — текст ответа админу на /promo. deliveryErr=nil
// если сообщение юзеру улетело успешно, иначе кратко описываем ошибку.
func (h *TelegramBotHandler) formatPromoIssueAdmin(
	target *promopb.LookupUserResponse,
	issue *promopb.IssuePromoResponse,
	promoURL string,
	deliveryErr error,
) string {
	var b strings.Builder
	displayName := promoTargetDisplay(target)
	if deliveryErr == nil {
		fmt.Fprintf(&b, "✅ Промо отправлен %s\n\n", displayName)
	} else {
		fmt.Fprintf(&b, "⚠️ Промо создан, но сообщение НЕ доставлено %s\n", displayName)
		fmt.Fprintf(&b, "Причина: %s\n", briefError(deliveryErr))
		b.WriteString("Можно скопировать ссылку ниже и отправить вручную.\n\n")
	}
	if issue.AlreadyExisted {
		b.WriteString("ℹ️ У юзера уже был активный промо — переиспользовали тот же токен.\n\n")
	}
	fmt.Fprintf(&b, "Промо: #%d\n", issue.PromoId)
	fmt.Fprintf(&b, "Token: %s\n", safePrefix(issue.Token))
	fmt.Fprintf(&b, "URL: %s\n", promoURL)
	if issue.ExpiresAtUnix > 0 {
		fmt.Fprintf(&b, "Истекает: %s\n", formatRelTime(issue.ExpiresAtUnix))
	}
	fmt.Fprintf(&b, "\nСтатус: /promo_status %s", displayName)
	return b.String()
}

// formatPromoStatusAdmin — текст для /promo_status. Содержит все ключевые
// маркеры: создан, истекает, оплачен, payment_id (для cross-ref в логах
// payment-service).
func formatPromoStatusAdmin(
	target *promopb.LookupUserResponse,
	st *promopb.GetPromoStatusResponse,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🎁 Promo #%d (%s)\n\n", st.PromoId, promoTargetDisplay(target))
	fmt.Fprintf(&b, "Token: %s\n", safePrefix(st.Token))
	fmt.Fprintf(&b, "Создан: %s\n", formatRelTime(st.CreatedAtUnix))
	if st.ExpiresAtUnix > 0 {
		fmt.Fprintf(&b, "Истекает: %s\n", formatRelTime(st.ExpiresAtUnix))
	}
	switch {
	case st.UsedAtUnix > 0:
		fmt.Fprintf(&b, "✅ Оплачен: %s (payment_id=%d)\n",
			formatRelTime(st.UsedAtUnix), st.PaymentId)
	case st.PaymentId > 0:
		fmt.Fprintf(&b, "🟡 Кликнул, оплата не подтверждена (payment_id=%d)\n", st.PaymentId)
	default:
		fmt.Fprintf(&b, "🕒 Не открывал (нет кликов)\n")
	}
	return b.String()
}

// briefError — короткое описание ошибки для Telegram alert. Без stack-trace.
func briefError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

// promoTargetDisplay — человекочитаемое имя для логов и сообщений админу.
// Предпочитаем @username (с @-префиксом), иначе fallback на telegram_id.
func promoTargetDisplay(t *promopb.LookupUserResponse) string {
	if t == nil {
		return "—"
	}
	if t.Username != "" {
		return "@" + t.Username
	}
	return fmt.Sprintf("tg:%d", t.TelegramId)
}
