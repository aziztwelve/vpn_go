# 15. Retention-кампании (trial-ending, onboarding-guide, churn-risk)

**Дата:** 2026-05-04
**Статус:** 🟡 Черновик — готов к имплементации после 06 Stage 1-2
**Автор:** Devin + aziz
**Родительский:** [06-traffic-caps.md](./06-traffic-caps.md) — использует `traffic_samples` / `users.{first_connection_at, last_traffic_at}` как источник сегментов
**Связано:** [05-trial-period.md](./05-trial-period.md) — триал-ending — главный триггер

---

## 🎯 Цель

Заменить ad-hoc bash-скрипт для рассылок (которым 2026-05-03 делали trial-ending broadcast) на нормальную систему:
1. **Segmentation engine** — cron, который каждые сутки пересегментирует юзеров на основе реального VPN-usage (`traffic_samples`, денормализованные поля в `users`).
2. **Draft-approve flow** — cron генерит черновики, Азиз approve'ит через команды в боте или admin-UI, после approve срабатывает sender.
3. **Delivery tracking** — `broadcast_sends` фиксирует per-recipient статус (sent/delivered/blocked/failed) + click/open tracking.
4. **История** — все кампании и отправки хранятся для анализа конверсии.

## 📚 Контекст

2026-05-03 сделали первую trial-ending рассылку 18 юзерам, bash-скрипт + curl на Bot API:
- 17/18 доставлено, 1 заблокировал бота
- Конверсия: 0% (0 оплат, 0 новых коннектов, 0 реф-кликов)
- Реально VPN использовал только 1/18, остальные даже не настроили клиент

Вывод по контексту: рассылки «оплати подписку» бессмысленны для юзеров, которые не настроили VPN. Нужны **разные сообщения для разных сегментов**:

| Сегмент | Условие | Сообщение |
|---|---|---|
| `trial_never_connected` | trial + created_at < NOW()-1h + `first_connection_at IS NULL` | Пошаговый гайд «как подключить за 30 сек» + ссылки на Happ/V2rayTUN + поддержка |
| `trial_ending_idle` | trial + expires_at < NOW()+24h + нет трафика >24h | Напоминание + гайд + «почему VPN не работает» FAQ |
| `trial_ending_active` | trial + expires_at < NOW()+24h + трафик ≥1 GB за 24h | Оплата-CTA с highlight их usage ("вы скачали 1.2 GB — продли чтобы не прерывать") |
| `paid_churn_risk` | active + последний трафик >3d назад | «Скучаем 🙂 проверь работает ли VPN» |
| `paid_ending_soon` | active + expires_at < NOW()+3d | Продление с бонусом |

---

## 🏗 Архитектура

```
                     ┌─────────────────────────────────────────┐
                     │  RetentionCron (gateway)                 │
                     │  тикер 1 раз в сутки, 17:00 МСК          │
                     │                                           │
                     │  for each segment (см. таблицу выше):    │
                     │    SELECT users WHERE <segment_filter>   │
                     │    IF count(recipients) > 0:             │
                     │      INSERT broadcast_drafts             │
                     │          (segment_key, title, body,      │
                     │           button_config, recipient_ids,  │
                     │           status='draft')                │
                     │      tgapi.sendMessage(admin_id,          │
                     │          "🆕 Draft #42 готов             │
                     │           Сегмент: trial_never_connected │
                     │           Получателей: 14                 │
                     │           Превью: ...                     │
                     │           [Approve] [Cancel]")           │
                     └──────────┬──────────────────────────────┘
                                │ trigger
                                ▼
                     ┌─────────────────────────────────────────┐
                     │  Admin action (Telegram callback OR API)│
                     │  /approve_42  или POST /admin/.../approve│
                     │  → broadcast_drafts.status = 'approved'  │
                     │  → запуск BroadcastSender                │
                     └──────────┬──────────────────────────────┘
                                ▼
                     ┌─────────────────────────────────────────┐
                     │  BroadcastSender (goroutine)             │
                     │  rate-limit 25 msg/s (TG global)         │
                     │  for each recipient_id:                  │
                     │    tgapi.sendMessage(user_id, text+btns) │
                     │    INSERT broadcast_sends(               │
                     │      broadcast_id, user_id, telegram_    │
                     │      message_id, status, sent_at)        │
                     │    handle 403 Forbidden → status=blocked │
                     │    handle 429 retry_after → sleep + retry│
                     │  on finish:                              │
                     │    UPDATE broadcast_drafts.status='sent' │
                     │    stats-сообщение админу                │
                     └─────────────────────────────────────────┘

            ┌─────────────────────────────────────────────┐
            │  CTA tracking                                │
            │  • web_app URL c ?ref=broadcast_42           │
            │  • frontend on mount:                        │
            │      POST /api/v1/analytics/broadcast-open  │
            │      → UPDATE broadcast_sends.opened_at      │
            │  • inline buttons callback:                  │
            │      callback_data=bc_42_subscribe           │
            │      → UPDATE broadcast_sends.clicked_at     │
            └─────────────────────────────────────────────┘
```

---

## 🧩 Изменения

### Stage 1 — миграции

**Локация:** gateway не имеет собственной БД-схемы (он pure HTTP shim, общается через gRPC). `broadcasts` кладём в `auth-service` — там уже `users`, `bot_starts`, `pending_referrals`, т.е. домены связанные с Telegram-identity. Gateway вызывает новый `BroadcastService` через gRPC.

**`services/auth-service/migrations/1777700000_add_broadcasts.up.sql`:**

```sql
CREATE TABLE broadcast_drafts (
    id BIGSERIAL PRIMARY KEY,
    segment_key VARCHAR(64) NOT NULL,                -- 'trial_never_connected' и т.д.
    title VARCHAR(255) NOT NULL,
    body_template TEXT NOT NULL,                      -- может содержать {{first_name}}, {{traffic_gb}}, {{ref_link}}
    button_config JSONB NOT NULL DEFAULT '[]'::jsonb, -- [{"text":"💎 Оформить","type":"web_app","url":"..."}]
    recipient_ids BIGINT[] NOT NULL,                  -- snapshot на момент создания
    recipient_count INTEGER NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'draft',      -- draft|approved|sending|sent|cancelled|failed
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at TIMESTAMPTZ,
    approved_by BIGINT REFERENCES users(id),
    sent_at TIMESTAMPTZ,
    notes TEXT
);

CREATE INDEX idx_broadcast_drafts_status ON broadcast_drafts(status);
CREATE INDEX idx_broadcast_drafts_segment ON broadcast_drafts(segment_key, created_at DESC);

CREATE TABLE broadcast_sends (
    id BIGSERIAL PRIMARY KEY,
    broadcast_id BIGINT NOT NULL REFERENCES broadcast_drafts(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    telegram_message_id BIGINT,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',   -- pending|sent|blocked|failed
    error_code INTEGER,                               -- TG API error_code
    error_message VARCHAR(255),
    sent_at TIMESTAMPTZ,
    opened_at TIMESTAMPTZ,                            -- webapp hit с ?ref=broadcast_X
    clicked_at TIMESTAMPTZ,
    cta_clicked VARCHAR(64),                          -- e.g. 'subscribe', 'invite_friend', 'support'
    UNIQUE (broadcast_id, user_id)
);

CREATE INDEX idx_broadcast_sends_broadcast ON broadcast_sends(broadcast_id, status);
CREATE INDEX idx_broadcast_sends_user ON broadcast_sends(user_id, sent_at DESC);
```

Down-миграция: `DROP TABLE IF EXISTS broadcast_sends, broadcast_drafts CASCADE;`

### Stage 2 — RetentionCron (gateway)

**`services/gateway/internal/service/retention_cron.go`** (новый):

```go
type Segment struct {
    Key           string
    Filter        string                 // SQL WHERE clause
    TitleTemplate string
    BodyTemplate  string
    Buttons       []ButtonConfig
    DailyCap      int                    // 0 = без cap, например 50 на запуск чтобы не взорваться
}

var segments = []Segment{
    {
        Key: "trial_never_connected",
        Filter: `
            s.status = 'trial'
            AND s.started_at <= NOW() - INTERVAL '1 hour'
            AND s.expires_at > NOW()
            AND u.first_connection_at IS NULL
            AND NOT EXISTS (
                SELECT 1 FROM broadcast_sends bs
                JOIN broadcast_drafts bd ON bd.id = bs.broadcast_id
                WHERE bs.user_id = u.id
                  AND bd.segment_key = 'trial_never_connected'
                  AND bs.sent_at > NOW() - INTERVAL '7 days'
            )`,
        TitleTemplate: "Onboarding: как подключить VPN",
        BodyTemplate: `Ассалому алейкум, {{first_name}}!

Вы включили пробный период MaydaVPN — но ещё не подключились ни к одному серверу.

Подключение за 30 секунд:
1. Установите Happ (рекомендуем) или V2rayTUN
2. Откройте MiniApp и нажмите «Подключить»
3. Выберите страну — готово

Нужна помощь — напишите @maydavpn_support, отвечаем в течение часа.`,
        Buttons: []ButtonConfig{
            {Text: "📲 Подключить VPN", Type: "web_app", URL: cfg.MiniAppURL + "?ref=broadcast_onboard"},
            {Text: "💬 Техподдержка", Type: "url", URL: "https://t.me/maydavpn_support"},
        },
        DailyCap: 50,
    },
    {
        Key: "trial_ending_idle",
        // trial заканчивается < 24h, трафика за сутки не было
        Filter: `
            s.status = 'trial'
            AND s.expires_at BETWEEN NOW() AND NOW() + INTERVAL '24 hours'
            AND (u.last_traffic_at IS NULL OR u.last_traffic_at < NOW() - INTERVAL '24 hours')`,
        TitleTemplate: "Завтра заканчивается триал",
        BodyTemplate: `Ассалому алейкум, {{first_name}}!

Завтра заканчивается пробный период MaydaVPN. Но похоже VPN ни разу не включился — давайте разберёмся?

Если что-то не получается — @maydavpn_support ответит за 5 минут.

Либо сразу оформите подписку — первый месяц со скидкой.`,
        Buttons: []ButtonConfig{
            {Text: "💎 Оформить подписку", Type: "web_app", URL: cfg.MiniAppURL + "/plans?ref=broadcast_trial_idle"},
            {Text: "💬 Написать в поддержку", Type: "url", URL: "https://t.me/maydavpn_support"},
        },
    },
    {
        Key: "trial_ending_active",
        // trial заканчивается < 24h, был трафик ≥1GB за 24ч
        Filter: `
            s.status = 'trial'
            AND s.expires_at BETWEEN NOW() AND NOW() + INTERVAL '24 hours'
            AND u.last_traffic_at >= NOW() - INTERVAL '24 hours'`,
        TitleTemplate: "Триал заканчивается — не прерывайте VPN",
        BodyTemplate: `Ассалому алейкум, {{first_name}}!

Завтра заканчивается пробный период. За последние сутки вы уже скачали через VPN ощутимо — чтобы не прерывать, продлите подписку сейчас.

Пригласите друга — получите ещё 3 дня бесплатно.`,
        Buttons: []ButtonConfig{
            {Text: "💎 Продлить подписку", Type: "web_app", URL: cfg.MiniAppURL + "/plans?ref=broadcast_trial_active"},
            {Text: "🎁 Пригласить друга (+3 дня)", Type: "url", URL: "{{ref_share_url}}"},
        },
    },
    {
        Key: "paid_churn_risk",
        // active-подписка, но трафика нет >3 дней
        Filter: `
            s.status = 'active'
            AND s.expires_at > NOW() + INTERVAL '3 days'
            AND (u.last_traffic_at IS NULL OR u.last_traffic_at < NOW() - INTERVAL '3 days')`,
        TitleTemplate: "Всё ок с VPN?",
        BodyTemplate: `Ассалому алейкум, {{first_name}}!

Заметили что давно не было подключений — всё ли в порядке с VPN?
Если проблемы — напишите @maydavpn_support, быстро починим.`,
        Buttons: []ButtonConfig{
            {Text: "💬 Техподдержка", Type: "url", URL: "https://t.me/maydavpn_support"},
        },
    },
}

func (c *RetentionCron) Run(ctx context.Context) {
    // daily at 17:00 MSK (14:00 UTC)
    for {
        wait := untilNext14UTC()
        select {
        case <-ctx.Done(): return
        case <-time.After(wait):
            c.generateDrafts(ctx)
        }
    }
}

func (c *RetentionCron) generateDrafts(ctx context.Context) {
    for _, seg := range segments {
        recipients := c.repo.SelectSegmentRecipients(ctx, seg.Filter, seg.DailyCap)
        if len(recipients) == 0 { continue }
        id := c.repo.InsertBroadcastDraft(ctx, seg, recipients)
        c.notifyAdmin(ctx, id, seg, recipients)
    }
}
```

### Stage 3 — BroadcastSender

**`services/gateway/internal/service/broadcast_sender.go`** (новый):

```go
type BroadcastSender struct {
    repo   *repository.BroadcastRepository
    tgapi  *telegram.Client
    logger *zap.Logger
}

func (s *BroadcastSender) Send(ctx context.Context, broadcastID int64) error {
    draft, err := s.repo.GetBroadcastWithRecipients(ctx, broadcastID)
    if err != nil { return err }
    if draft.Status != "approved" { return ErrNotApproved }

    s.repo.UpdateStatus(ctx, broadcastID, "sending")
    limiter := rate.NewLimiter(rate.Limit(25), 1)  // 25 msg/s global TG limit

    var sent, blocked, failed int
    for _, uid := range draft.RecipientIDs {
        if err := limiter.Wait(ctx); err != nil { return err }

        user, _ := s.repo.GetUser(ctx, uid)
        text := renderTemplate(draft.BodyTemplate, user)
        buttons := renderButtons(draft.ButtonConfig, user)

        msg, err := s.tgapi.SendMessage(ctx, user.TelegramID, text, buttons)
        send := &BroadcastSend{BroadcastID: broadcastID, UserID: uid, SentAt: time.Now()}

        switch {
        case err == nil:
            send.Status = "sent"
            send.TelegramMessageID = msg.MessageID
            sent++
        case telegram.IsForbidden(err):
            send.Status = "blocked"
            blocked++
        default:
            send.Status = "failed"
            send.ErrorMessage = err.Error()
            failed++
        }
        s.repo.InsertSend(ctx, send)
    }

    s.repo.UpdateStatus(ctx, broadcastID, "sent")
    s.notifyAdminFinish(ctx, broadcastID, sent, blocked, failed)
    return nil
}
```

### Stage 4 — Admin handlers

**`services/gateway/internal/handler/admin_broadcast.go`** (новый):

```go
// GET    /api/v1/admin/broadcasts                      — список (с фильтрами по status)
// GET    /api/v1/admin/broadcasts/{id}                 — детали + stats из broadcast_sends
// POST   /api/v1/admin/broadcasts/{id}/approve         — approve + запуск sender'а
// POST   /api/v1/admin/broadcasts/{id}/cancel          — отменить draft
// PATCH  /api/v1/admin/broadcasts/{id}                 — отредактировать title/body/buttons до approve
```

Auth middleware: `users.role = 'admin'`. Список admin-telegram-id из конфига — cross-check с `admin_telegram_ids` как гарантия.

### Stage 5 — Telegram bot commands

Расширить `services/gateway/internal/handler/telegram_bot.go`:

- `/admin` — показывает меню с pending drafts и счётчиками.
- `/approve_<id>` — approve draft, sender запускается в goroutine.
- `/cancel_<id>` — отмена.
- `/broadcast_stats <id>` — статы по отправке.
- Callback-кнопки в notify-сообщении с draft-превью: `bc_approve_<id>`, `bc_cancel_<id>`.

Все команды только для юзеров с `role='admin'`.

### Stage 6 — CTA click tracking

- Все web_app URL в buttons → добавляется `?ref=broadcast_<id>`.
- `vpn_next`: в корневом layout если есть query-param `ref=broadcast_*`, дёрнуть `POST /api/v1/analytics/broadcast-open {ref: "broadcast_42"}`.
- Новый handler `/api/v1/analytics/broadcast-open` — достаёт `user_id` из init-data Telegram WebApp, UPDATE `broadcast_sends.opened_at = NOW()` по `(broadcast_id, user_id)`.
- Для inline-кнопок с `callback_data` — обрабатывается уже в `telegram_bot.go`, расширяем на `bc_*` префикс.

---

## ✅ Definition of Done

- [ ] Миграция broadcasts применена (dev + prod)
- [ ] RetentionCron запущен в gateway, ежедневно в 17:00 МСК пушит Азизу в бот драфты по сегментам
- [ ] На 2026-05-04 cron сегментирует текущие 132 юзера, значения ожидаемые (≥10 в `trial_never_connected` на основе вчерашних 12 из 18)
- [ ] `/approve_<id>` + `POST /admin/broadcasts/{id}/approve` — оба работают, sender шлёт с rate-limit, blocked юзеры корректно маркируются
- [ ] `broadcast_sends.opened_at` заполняется при открытии web_app с `?ref=broadcast_X`
- [ ] Старый bash-скрипт trial-ending broadcast удалён из проектных ссылок, все рассылки через этот механизм
- [ ] В `memory/` запись: первые 2-3 кампании с метриками open-rate / CTR / conversion

## ⚠️ Риски

| Риск | Митигация |
|---|---|
| Генерим draft каждый день одним и тем же юзерам | В segment-filter — `NOT EXISTS broadcast_sends за последние 7 дней с тем же segment_key`. Для trial-ending — можно ослабить до 2 дней. |
| Админ спит, draft висит несколько дней, данные устарели | TTL: drafts со status=draft старше 48h — автоматически cancelled. RetentionCron при следующем запуске сгенерирует новый. |
| Rate-limit Telegram | `golang.org/x/time/rate.Limiter(25/s)` + retry на 429 с учётом `retry_after`. |
| Telegram returns `peer_id_invalid` (юзер удалил аккаунт) | Catch отдельно, status='failed', error_code=400, не блочит остальных. |
| Approve-флоу кажется тяжёлым — если рассылки каждый день | Добавить `segments[i].AutoApprove bool` позже для проверенных сегментов с низким риском (e.g. `trial_ending_active` с N<20). |
| Персональные данные в broadcast_drafts.recipient_ids (BIGINT[]) | Внутренняя база, admin-only доступ. Не экспортируем. |
| Ошибки в шаблонах (битый `{{first_name}}`) | Unit-тест `renderTemplate` для всех сегментов + превью в notify перед approve. |
| sending-статус застревает при падении sender'а | При старте gateway — `UPDATE broadcast_drafts SET status='draft' WHERE status='sending'` (resume-able), либо транзакционно блокировать row. |

## 🤔 Открытые вопросы

1. **Admin UI vs только команды бота?**
   - MVP: команды бота (`/approve_<id>`). Список — `GET /admin/broadcasts` в JSON, Азиз курлит.
   - Полноценный UI в `vpn_next/app/admin/` — опционально, отдельной задачей.
   
2. **Частота RetentionCron?**
   - Раз в сутки достаточно для trial-flow (триал 3 дня, окна в сутки).
   - Для `paid_churn_risk` можно реже (раз в 3 дня).
   - Пока one-size 1/сутки, разбиение позже.

3. **Мультиязычность (RU/UZ/TJ)?**
   - Сейчас все сообщения на RU. По `users.language_code` можно шаблонизировать — но **для MVP оставим RU** (132 юзера, аудитория понимает).

4. **Templating — plain string vs proper engine?**
   - MVP: `strings.ReplaceAll` для `{{first_name}}`, `{{traffic_gb}}`, `{{ref_share_url}}`.
   - Позже: `text/template` с защитами (escape HTML).

5. **Ошибки доставки — ретраи?**
   - `blocked` → не ретраим.
   - `failed` с 500/network — ретрай 3 раза с backoff, потом окончательный failed.

6. **Broadcast-history retention?**
   - `broadcast_sends` пухнет. После 90 дней можно архивировать в `broadcast_sends_archive` или TRUNCATE old. Решим при появлении проблем.

## 🔗 Ссылки

- Родительский: [06-traffic-caps.md](./06-traffic-caps.md) — инфра traffic_samples, без которой retention-сегментация невозможна
- `platform/pkg/telegram/client.go` — существующий TG-клиент, нужен метод `SendMessage` с кнопками (может уже есть)
- 2026-05-03 ad-hoc bash-скрипт (в `memory/2026-05-03.md`) — факт который мотивирует этот таск
