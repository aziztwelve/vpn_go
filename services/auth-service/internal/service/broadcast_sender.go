// Package service — broadcast_sender.go: фактическая рассылка retention-сообщений.
//
// Workflow:
//   1. Approve через gRPC ApproveBroadcast → api/broadcast.go вызывает
//      Sender.Send в фоновой горутине, а сам сразу возвращает gateway.
//   2. Sender читает draft (с recipient_ids snapshot'ом), переводит в
//      status='sending'.
//   3. Итерирует recipients с rate-limit 25 msg/s (Telegram global limit).
//      Per-user: GetUserForSend → render template + buttons → SendMessage →
//      InsertSend(status='sent'/'blocked'/'failed').
//   4. По окончании: status='sent' (даже если часть failed — это финальный
//      терминальный статус), notify админам summary.
//
// Resume-after-restart: если sender упал в середине (status застрял в
// 'sending'), при следующем старте auth-service в Stop() / startup-cleanup
// пока ничего не делается. Это TODO Stage 6 — пока админ может вручную
// SQL'ом UPDATE статус и retry. На 100-200 recipients sender работает
// 4-8 секунд, риск падения посередине минимальный.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/vpn/auth-service/internal/repository"
	"github.com/vpn/platform/pkg/telegram"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// SendRateLimitPerSec — Telegram bot API global limit ≈ 30 msg/s.
// Берём 25 для запаса (другие сообщения от бота не попадают под этот
// rate-limit, Stars-инвойсы / approve-replies / etc).
const SendRateLimitPerSec = 25

// BroadcastSender — состояние сендера. Stateless по сути (всё в БД), но
// держим dependency-объекты как поля.
type BroadcastSender struct {
	repo   *repository.BroadcastRepository
	tg     *telegram.Client
	logger *zap.Logger

	// inFlight — защита от двойного approve одного и того же draft'а.
	// Если же ApproveBroadcast приходит дважды (две одинаковых callback'и
	// от Telegram при retry), вторая попадёт в эту проверку и будет no-op.
	mu       sync.Mutex
	inFlight map[int64]bool
}

func NewBroadcastSender(
	repo *repository.BroadcastRepository,
	tg *telegram.Client,
	logger *zap.Logger,
) *BroadcastSender {
	return &BroadcastSender{
		repo:     repo,
		tg:       tg,
		logger:   logger,
		inFlight: make(map[int64]bool),
	}
}

// SendResult — суммарная статистика. Возвращается по завершении Send.
type SendResult struct {
	DraftID   int64
	Total     int
	Sent      int
	Blocked   int
	Failed    int
	Elapsed   time.Duration
}

// Send рассылает draft в фоне. Блокирующий вызов — ApproveBroadcast в API
// должен запускать в горутине. ctx должен быть detached от gRPC-запроса
// (api/broadcast.go использует context.Background()), потому что иначе
// gateway-callback таймаутит и контекст отменится посреди рассылки.
//
// Идемпотентность по статусу: ожидает статус='approved' (т.е. callsite уже
// сделал Approve в той же транзакции, что и trigger). Если статус другой —
// пишет ошибку и выходит.
func (s *BroadcastSender) Send(ctx context.Context, draftID int64) (*SendResult, error) {
	// Захватываем in-flight slot.
	s.mu.Lock()
	if s.inFlight[draftID] {
		s.mu.Unlock()
		return nil, errors.New("broadcast already sending")
	}
	s.inFlight[draftID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inFlight, draftID)
		s.mu.Unlock()
	}()

	start := time.Now()
	draft, err := s.repo.GetDraft(ctx, draftID)
	if err != nil {
		return nil, fmt.Errorf("load draft: %w", err)
	}

	// Переход 'approved' → 'sending'. Если кто-то параллельно отменил —
	// строк затронуто 0, выходим без отправки.
	affected, err := s.repo.UpdateDraftStatus(ctx, draftID, "approved", "sending", 0)
	if err != nil {
		return nil, fmt.Errorf("set sending: %w", err)
	}
	if affected == 0 {
		return nil, fmt.Errorf("draft %d not in approved status (current: %s)", draftID, draft.Status)
	}

	s.logger.Info("broadcast: starting",
		zap.Int64("draft_id", draftID),
		zap.String("segment", draft.SegmentKey),
		zap.Int("recipients", draft.RecipientCount),
	)

	limiter := rate.NewLimiter(rate.Limit(SendRateLimitPerSec), 1)
	res := &SendResult{DraftID: draftID, Total: len(draft.RecipientIDs)}

	for i, uid := range draft.RecipientIDs {
		if err := limiter.Wait(ctx); err != nil {
			s.logger.Warn("broadcast: rate-limit wait interrupted",
				zap.Error(err), zap.Int("processed", i))
			break
		}
		s.sendOne(ctx, draft, uid, res)
	}

	res.Elapsed = time.Since(start)

	// Финальный статус — 'sent' (даже если все blocked/failed). Это
	// корректнее чем 'failed', т.к. job отработал; per-user статусы
	// в broadcast_sends.
	if _, err := s.repo.UpdateDraftStatus(ctx, draftID, "sending", "sent", 0); err != nil {
		s.logger.Error("broadcast: set final status failed",
			zap.Int64("draft_id", draftID), zap.Error(err))
	}

	s.logger.Info("broadcast: done",
		zap.Int64("draft_id", draftID),
		zap.Int("total", res.Total),
		zap.Int("sent", res.Sent),
		zap.Int("blocked", res.Blocked),
		zap.Int("failed", res.Failed),
		zap.Duration("elapsed", res.Elapsed),
	)

	// Notify админам summary. Не критично — ошибка только логируется.
	s.notifyFinish(ctx, draft, res)

	return res, nil
}

// sendOne — отправка одному юзеру с записью в broadcast_sends.
// Любая ошибка ловится и записывается в БД; outer loop не падает.
func (s *BroadcastSender) sendOne(
	ctx context.Context,
	draft *repository.Draft,
	userID int64,
	res *SendResult,
) {
	user, err := s.repo.GetUserForSend(ctx, userID)
	if err != nil {
		_ = s.repo.InsertSend(ctx, repository.SendInput{
			BroadcastID:  draft.ID,
			UserID:       userID,
			Status:       "failed",
			ErrorMessage: "user lookup failed: " + err.Error(),
		})
		res.Failed++
		return
	}
	if user.IsBanned {
		_ = s.repo.InsertSend(ctx, repository.SendInput{
			BroadcastID:  draft.ID,
			UserID:       userID,
			Status:       "failed",
			ErrorMessage: "user banned",
		})
		res.Failed++
		return
	}

	text := renderTemplate(draft.BodyTemplate, user)
	kb := renderKeyboard(draft.ButtonConfig, user, draft.ID)

	err = s.tg.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:                user.TelegramID,
		Text:                  text,
		ParseMode:             "", // plain text — у нас простые шаблоны без HTML
		ReplyMarkup:           kb,
		DisableWebPagePreview: true,
	})
	in := repository.SendInput{BroadcastID: draft.ID, UserID: userID}
	if err == nil {
		in.Status = "sent"
		res.Sent++
	} else if isBlockedError(err) {
		in.Status = "blocked"
		in.ErrorMessage = truncate(err.Error(), 500)
		res.Blocked++
	} else {
		in.Status = "failed"
		in.ErrorMessage = truncate(err.Error(), 500)
		res.Failed++
		s.logger.Warn("broadcast: send failed",
			zap.Int64("draft_id", draft.ID),
			zap.Int64("user_id", userID),
			zap.Int64("tg_id", user.TelegramID),
			zap.Error(err),
		)
	}
	if err := s.repo.InsertSend(ctx, in); err != nil {
		s.logger.Error("broadcast: insert send failed",
			zap.Int64("draft_id", draft.ID),
			zap.Int64("user_id", userID),
			zap.Error(err),
		)
	}
}

// notifyFinish шлёт админам summary окончания рассылки.
func (s *BroadcastSender) notifyFinish(ctx context.Context, draft *repository.Draft, res *SendResult) {
	admins, err := s.repo.ListAdminTelegramIDs(ctx)
	if err != nil || len(admins) == 0 {
		return
	}
	text := fmt.Sprintf(
		"📤 Рассылка завершена\n\n"+
			"Сегмент: %s\nDraft: #%d\n"+
			"Всего: %d • Sent: %d • Blocked: %d • Failed: %d\n"+
			"Длительность: %s",
		draft.SegmentKey, draft.ID,
		res.Total, res.Sent, res.Blocked, res.Failed,
		res.Elapsed.Round(time.Second),
	)
	for _, tgID := range admins {
		if err := s.tg.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: tgID, Text: text, DisableWebPagePreview: true,
		}); err != nil {
			s.logger.Warn("broadcast: notify finish failed",
				zap.Int64("admin_tg_id", tgID), zap.Error(err))
		}
	}
}

// renderTemplate подставляет {{first_name}} / {{username}} в шаблон.
// Простой strings.ReplaceAll достаточно для текущих 4 сегментов; если
// шаблоны усложнятся — переедем на text/template с защитой.
func renderTemplate(tmpl string, u *repository.UserForSend) string {
	first := u.FirstName
	if first == "" {
		first = "друг"
	}
	out := strings.ReplaceAll(tmpl, "{{first_name}}", first)
	out = strings.ReplaceAll(out, "{{username}}", u.Username)
	return out
}

// renderKeyboard конвертирует button_config в Telegram InlineKeyboardMarkup.
// Per-user URL-замены (например {{ref_share_url}}) делаем тут.
func renderKeyboard(buttons []repository.ButtonConfig, u *repository.UserForSend, draftID int64) *telegram.InlineKeyboardMarkup {
	if len(buttons) == 0 {
		return nil
	}
	rows := make([][]telegram.InlineKeyboardButton, 0, len(buttons))
	for _, b := range buttons {
		btn := telegram.InlineKeyboardButton{Text: b.Text}
		url := strings.ReplaceAll(b.URL, "{{username}}", u.Username)
		// {{ref_share_url}} — placeholder для будущей рефералки. Сейчас
		// просто в no-op заменим, чтобы битая строка не пошла в TG.
		url = strings.ReplaceAll(url, "{{ref_share_url}}", "")

		switch b.Type {
		case "web_app":
			btn.WebApp = &telegram.WebAppInfo{URL: url}
		case "url":
			btn.URL = url
		case "callback_data":
			btn.CallbackData = b.Data
		default:
			// Неизвестный тип — пропускаем кнопку, но не валим всю кнопку-
			// клавиатуру. Логировать уровнем выше; здесь это статически
			// заданные шаблоны RetentionCron'ом, опечатка ловится при review.
			continue
		}
		rows = append(rows, []telegram.InlineKeyboardButton{btn})
	}
	if len(rows) == 0 {
		return nil
	}
	_ = draftID // зарезервировано на будущее — для CTA tracking (Stage 6)
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// isBlockedError — детектит «юзер заблокировал бота» (HTTP 403 Forbidden от
// Telegram). Telegram возвращает это в виде строки с описанием. Стандартного
// типизированного error от platform/pkg/telegram/client.go нет — парсим
// текст. Это ОК для prod, потому что Telegram error-формат стабилен годами.
func isBlockedError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Forbidden: bot was blocked") ||
		strings.Contains(msg, "Forbidden: user is deactivated") ||
		strings.Contains(msg, "Forbidden: bot can't initiate conversation") ||
		strings.Contains(msg, "Forbidden: bot was kicked")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
