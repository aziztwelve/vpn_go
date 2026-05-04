// Package service — retention_cron.go: ежедневная генерация retention-рассылок.
//
// Каждые сутки в 14:00 UTC (17:00 МСК) RetentionCron перебирает 4 сегмента
// юзеров (trial_never_connected, trial_ending_idle, trial_ending_active,
// paid_churn_risk). Для каждого непустого сегмента создаётся broadcast_draft
// в status='draft' с snapshot'ом recipient_ids, и админам в Telegram
// пушится превью с inline-кнопками Approve/Cancel.
//
// Сам sender (после approve) будет в Stage 3. Пока approve делается руками
// через SQL UPDATE broadcast_drafts SET status='approved' + триггер rebuild.
//
// Дизайн-решения:
//   - Cron живёт в auth-service, а не в gateway. Исходный спек
//     (15-retention-campaigns.md) упоминал gateway, но gateway — pure HTTP
//     shim без БД-доступа. users / subscriptions / broadcasts — всё здесь.
//   - Фильтры сегментов — статические SQL-методы в repository/broadcast.go
//     (не params), защита от injection + читаемость.
//   - Overlap между сегментами исключается в самих фильтрах
//     (trial_never_connected требует expires_at > 24h вперёд), плюс
//     глобальный rate-limit «не чаще 1 retention в 24h на юзера».
//   - RETENTION_CRON_ENABLED — env-флаг для прода (default TRUE).
//     RETENTION_CRON_AT_UTC — "HH:MM" (default "14:00"), для отладки можно
//     временно поставить скорое время.
package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vpn/auth-service/internal/repository"
	"github.com/vpn/platform/pkg/telegram"
	"go.uber.org/zap"
)

// segment — описание одного сегмента.
type segment struct {
	key          string
	title        string
	bodyTemplate string
	buttons      []repository.ButtonConfig
	dailyCap     int // 0 = без cap
	selector     func(ctx context.Context, limit int) ([]int64, error)
}

// RetentionCron генерирует retention-рассылки. Не отправляет сообщения сам
// (это Stage 3) — только создаёт drafts и уведомляет админов.
type RetentionCron struct {
	repo     *repository.BroadcastRepository
	tg       *telegram.Client // для notify админам
	miniApp  string           // cfg.MiniAppURL — базовый URL MiniApp
	supportU string           // username поддержки (без @) для кнопки "Техподдержка"
	runAtUTC string           // "HH:MM" UTC
	logger   *zap.Logger

	segments []segment
}

// Config — параметры из env (пробрасывается из app.Config).
type RetentionCronConfig struct {
	Enabled         bool
	RunAtUTC        string // "HH:MM"
	MiniAppURL      string
	SupportUsername string // без @
}

func NewRetentionCron(
	repo *repository.BroadcastRepository,
	tg *telegram.Client,
	cfg RetentionCronConfig,
	logger *zap.Logger,
) *RetentionCron {
	c := &RetentionCron{
		repo:     repo,
		tg:       tg,
		miniApp:  strings.TrimRight(cfg.MiniAppURL, "/"),
		supportU: strings.TrimPrefix(cfg.SupportUsername, "@"),
		runAtUTC: cfg.RunAtUTC,
		logger:   logger,
	}
	c.segments = c.buildSegments()
	return c
}

// Run блокируется до ctx.Done(). Каждые сутки в runAtUTC запускает tick.
// Первый tick — в ближайшее следующее совпадение времени (не при старте),
// чтобы перезапуски днём не ретриггерили генерацию.
func (c *RetentionCron) Run(ctx context.Context) {
	c.logger.Info("retention cron started",
		zap.String("run_at_utc", c.runAtUTC),
		zap.Int("segments", len(c.segments)))

	for {
		wait, err := c.untilNextRun(time.Now().UTC())
		if err != nil {
			c.logger.Error("retention cron: parse run_at_utc failed, stopping",
				zap.Error(err), zap.String("run_at_utc", c.runAtUTC))
			return
		}
		c.logger.Info("retention cron: sleeping until next run",
			zap.Duration("wait", wait))

		select {
		case <-ctx.Done():
			c.logger.Info("retention cron stopped")
			return
		case <-time.After(wait):
			c.tick(ctx)
		}
	}
}

// untilNextRun считает время до следующего HH:MM UTC. Если сейчас уже после
// HH:MM сегодня — ждём до завтра.
func (c *RetentionCron) untilNextRun(now time.Time) (time.Duration, error) {
	t, err := time.Parse("15:04", c.runAtUTC)
	if err != nil {
		return 0, fmt.Errorf("parse run_at_utc %q: %w", c.runAtUTC, err)
	}
	next := time.Date(now.Year(), now.Month(), now.Day(),
		t.Hour(), t.Minute(), 0, 0, time.UTC)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now), nil
}

// tick — одна ежедневная итерация: сегментирует, создаёт drafts, нотифит.
// Ошибка на одном сегменте не роняет остальные.
func (c *RetentionCron) tick(ctx context.Context) {
	start := time.Now()
	c.logger.Info("retention cron tick started")

	var results []segmentResult
	for _, seg := range c.segments {
		res := c.processSegment(ctx, seg)
		results = append(results, res)
	}

	// Notify admins одним суммирующим сообщением + отдельными сообщениями
	// per-draft с кнопками approve/cancel (когда Stage 5 подъедет, они
	// начнут работать; сейчас кнопки callback_data просто логируются в
	// handler'е gateway, надо будет расширить).
	if err := c.notifyAdmins(ctx, results); err != nil {
		c.logger.Warn("retention cron: admin notify failed",
			zap.Error(err))
	}

	c.logger.Info("retention cron tick done",
		zap.Duration("elapsed", time.Since(start)),
		zap.Any("results", results),
	)
}

type segmentResult struct {
	Key        string  `json:"key"`
	DraftID    int64   `json:"draft_id,omitempty"`
	Recipients int     `json:"recipients"`
	Error      string  `json:"error,omitempty"`
}

func (c *RetentionCron) processSegment(ctx context.Context, seg segment) segmentResult {
	res := segmentResult{Key: seg.key}

	recipients, err := seg.selector(ctx, seg.dailyCap)
	if err != nil {
		c.logger.Error("retention cron: segment selector failed",
			zap.String("segment", seg.key), zap.Error(err))
		res.Error = err.Error()
		return res
	}
	res.Recipients = len(recipients)
	if len(recipients) == 0 {
		return res
	}

	id, err := c.repo.InsertBroadcastDraft(ctx, repository.DraftInput{
		SegmentKey:   seg.key,
		Title:        seg.title,
		BodyTemplate: seg.bodyTemplate,
		Buttons:      seg.buttons,
		RecipientIDs: recipients,
	})
	if err != nil {
		c.logger.Error("retention cron: insert draft failed",
			zap.String("segment", seg.key), zap.Error(err))
		res.Error = err.Error()
		return res
	}
	res.DraftID = id
	c.logger.Info("retention cron: draft created",
		zap.String("segment", seg.key),
		zap.Int64("draft_id", id),
		zap.Int("recipients", len(recipients)),
	)
	return res
}

// notifyAdmins шлёт одно summary-сообщение каждому админу с кнопками
// approve/cancel под каждым непустым draft'ом.
func (c *RetentionCron) notifyAdmins(ctx context.Context, results []segmentResult) error {
	admins, err := c.repo.ListAdminTelegramIDs(ctx)
	if err != nil {
		return fmt.Errorf("list admins: %w", err)
	}
	if len(admins) == 0 {
		c.logger.Warn("retention cron: no admins registered, notify skipped " +
			"(set users.role='admin' for at least one user)")
		return nil
	}

	text := c.renderNotifyText(results)
	if text == "" {
		return nil // все сегменты пустые, нечего слать
	}
	kb := c.renderNotifyKeyboard(results)

	for _, tgID := range admins {
		err := c.tg.SendMessage(ctx, telegram.SendMessageParams{
			ChatID:                tgID,
			Text:                  text,
			ParseMode:             "HTML",
			ReplyMarkup:           kb,
			DisableWebPagePreview: true,
		})
		if err != nil {
			c.logger.Warn("retention cron: notify admin failed",
				zap.Int64("admin_tg_id", tgID), zap.Error(err))
		}
	}
	return nil
}

func (c *RetentionCron) renderNotifyText(results []segmentResult) string {
	var any bool
	var b strings.Builder
	b.WriteString("🎯 <b>Retention-дайджест</b>\n\n")
	for _, r := range results {
		if r.Recipients == 0 && r.Error == "" {
			continue
		}
		any = true
		if r.Error != "" {
			fmt.Fprintf(&b, "• <code>%s</code>: ❌ %s\n", r.Key, r.Error)
			continue
		}
		fmt.Fprintf(&b, "• <code>%s</code>: %d получателей — draft <b>#%d</b>\n",
			r.Key, r.Recipients, r.DraftID)
	}
	if !any {
		return ""
	}
	b.WriteString("\nApprove/cancel под каждым драфтом. ")
	b.WriteString("До approve — сообщения не отправляются.")
	return b.String()
}

func (c *RetentionCron) renderNotifyKeyboard(results []segmentResult) *telegram.InlineKeyboardMarkup {
	var rows [][]telegram.InlineKeyboardButton
	for _, r := range results {
		if r.DraftID == 0 {
			continue
		}
		rows = append(rows, []telegram.InlineKeyboardButton{
			{Text: fmt.Sprintf("✅ Approve #%d", r.DraftID),
				CallbackData: fmt.Sprintf("bc_approve_%d", r.DraftID)},
			{Text: fmt.Sprintf("❌ Cancel #%d", r.DraftID),
				CallbackData: fmt.Sprintf("bc_cancel_%d", r.DraftID)},
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// buildSegments — определение всех сегментов. Порядок имеет значение
// только для notify-сообщения (в таком же порядке пойдут); для самой
// генерации — безразлично (селекторы независимые).
func (c *RetentionCron) buildSegments() []segment {
	supportURL := "https://t.me/maydavpn_support"
	if c.supportU != "" {
		supportURL = "https://t.me/" + c.supportU
	}

	return []segment{
		{
			key:   "trial_never_connected",
			title: "Onboarding: как подключить VPN",
			bodyTemplate: "Ассалому алейкум, {{first_name}}!\n\n" +
				"Вы включили пробный период MaydaVPN — но ещё не подключились ни к одному серверу.\n\n" +
				"Подключение за 30 секунд:\n" +
				"1. Установите Happ (рекомендуем) или V2rayTUN\n" +
				"2. Откройте MiniApp и нажмите «Подключить»\n" +
				"3. Выберите страну — готово\n\n" +
				"Нужна помощь — напишите в поддержку, отвечаем в течение часа.",
			buttons: []repository.ButtonConfig{
				{Text: "📲 Подключить VPN", Type: "web_app",
					URL: c.miniApp + "?ref=broadcast_onboard"},
				{Text: "💬 Техподдержка", Type: "url", URL: supportURL},
			},
			dailyCap: 50,
			selector: c.repo.SelectTrialNeverConnected,
		},
		{
			key:   "trial_ending_idle",
			title: "Завтра заканчивается триал",
			bodyTemplate: "Ассалому алейкум, {{first_name}}!\n\n" +
				"Завтра заканчивается пробный период MaydaVPN. Но похоже VPN ни разу не включился — давайте разберёмся?\n\n" +
				"Если что-то не получается — поддержка ответит за 5 минут.\n" +
				"Либо сразу оформите подписку — первый месяц со скидкой.",
			buttons: []repository.ButtonConfig{
				{Text: "💎 Оформить подписку", Type: "web_app",
					URL: c.miniApp + "/plans?ref=broadcast_trial_idle"},
				{Text: "💬 Написать в поддержку", Type: "url", URL: supportURL},
			},
			dailyCap: 0, // все — их мало и они срочные
			selector: c.repo.SelectTrialEndingIdle,
		},
		{
			key:   "trial_ending_active",
			title: "Триал заканчивается — не прерывайте VPN",
			bodyTemplate: "Ассалому алейкум, {{first_name}}!\n\n" +
				"Завтра заканчивается пробный период. За последние сутки вы уже скачали через VPN ощутимо — чтобы не прерывать, продлите подписку сейчас.",
			buttons: []repository.ButtonConfig{
				{Text: "💎 Продлить подписку", Type: "web_app",
					URL: c.miniApp + "/plans?ref=broadcast_trial_active"},
				{Text: "💬 Поддержка", Type: "url", URL: supportURL},
			},
			dailyCap: 0,
			selector: c.repo.SelectTrialEndingActive,
		},
		{
			key:   "paid_churn_risk",
			title: "Всё ок с VPN?",
			bodyTemplate: "Ассалому алейкум, {{first_name}}!\n\n" +
				"Заметили что давно не было подключений — всё ли в порядке с VPN?\n" +
				"Если проблемы — напишите в поддержку, быстро починим.",
			buttons: []repository.ButtonConfig{
				{Text: "💬 Техподдержка", Type: "url", URL: supportURL},
			},
			dailyCap: 0,
			selector: c.repo.SelectPaidChurnRisk,
		},
	}
}
