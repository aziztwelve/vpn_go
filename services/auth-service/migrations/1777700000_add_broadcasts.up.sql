-- Таблицы для retention-рассылок (trial-ending, onboarding-гайд,
-- churn-risk). Заменяют ad-hoc bash-скрипт, которым 2026-05-03 делали
-- рассылку 18 триал-юзерам.
--
-- Flow:
--   1. RetentionCron в gateway (cron раз в сутки 17:00 МСК) сегментирует
--      юзеров по состоянию (триал/платка, есть ли трафик, когда был и т.д.)
--      и для каждого непустого сегмента создаёт ЧЕРНОВИК — broadcast_drafts
--      (status='draft', recipient_ids snapshot).
--   2. Админу (Азизу) в личку бота приходит превью + кнопки Approve/Cancel.
--   3. При approve: draft.status='approved' → BroadcastSender рассылает с
--      rate-limit 25 msg/s → для каждого получателя пишется broadcast_sends
--      со статусом sent/blocked/failed.
--   4. При открытии MiniApp c ?ref=broadcast_<id> → UPDATE broadcast_sends
--      .opened_at. Клик inline-кнопки с callback_data=bc_<id>_<action> →
--      .clicked_at + .cta_clicked.
--
-- Локация в auth-service, а не в gateway: gateway — pure HTTP shim без
-- БД-доступа, broadcasts логично живут рядом с users / bot_starts в
-- auth-service (схожие Telegram-identity домены).

CREATE TABLE IF NOT EXISTS broadcast_drafts (
    id              BIGSERIAL PRIMARY KEY,
    segment_key     VARCHAR(64) NOT NULL,            -- trial_never_connected, paid_churn_risk, ...
    title           VARCHAR(255) NOT NULL,
    body_template   TEXT NOT NULL,                   -- c подстановками {{first_name}}, {{traffic_gb}}
    button_config   JSONB NOT NULL DEFAULT '[]'::jsonb,
                    -- [{"text":"💎 Оформить","type":"web_app","url":"..."},
                    --  {"text":"💬 Поддержка","type":"url","url":"..."}]
    recipient_ids   BIGINT[] NOT NULL,               -- snapshot user.id на момент генерации
    recipient_count INTEGER NOT NULL,
    status          VARCHAR(32) NOT NULL DEFAULT 'draft',
                    -- draft | approved | sending | sent | cancelled | failed
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    approved_at     TIMESTAMPTZ,
    approved_by     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    sent_at         TIMESTAMPTZ,
    notes           TEXT,
    CHECK (status IN ('draft','approved','sending','sent','cancelled','failed'))
);

CREATE INDEX IF NOT EXISTS idx_broadcast_drafts_status
    ON broadcast_drafts(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_broadcast_drafts_segment
    ON broadcast_drafts(segment_key, created_at DESC);

COMMENT ON TABLE broadcast_drafts IS
    'Retention campaigns: generated drafts awaiting admin approval.';
COMMENT ON COLUMN broadcast_drafts.recipient_ids IS
    'Snapshot of user ids at generation time. Not recomputed at send time — '
    'if user changes status between draft and approve, они ВСЁ ЕЩЁ получат.';

CREATE TABLE IF NOT EXISTS broadcast_sends (
    id                  BIGSERIAL PRIMARY KEY,
    broadcast_id        BIGINT NOT NULL REFERENCES broadcast_drafts(id) ON DELETE CASCADE,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    telegram_message_id BIGINT,
    status              VARCHAR(32) NOT NULL DEFAULT 'pending',
                        -- pending | sent | blocked | failed
    error_code          INTEGER,                     -- TG error_code
    error_message       VARCHAR(512),
    sent_at             TIMESTAMPTZ,
    opened_at           TIMESTAMPTZ,                 -- MiniApp hit с ?ref=broadcast_X
    clicked_at          TIMESTAMPTZ,
    cta_clicked         VARCHAR(64),                 -- subscribe | invite | support | ...
    CHECK (status IN ('pending','sent','blocked','failed')),
    UNIQUE (broadcast_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_broadcast_sends_broadcast_status
    ON broadcast_sends(broadcast_id, status);
CREATE INDEX IF NOT EXISTS idx_broadcast_sends_user
    ON broadcast_sends(user_id, sent_at DESC);

-- Partial-индекс для быстрой дедупликации в segment-фильтре
--   "NOT EXISTS send with same segment in last 7 days":
CREATE INDEX IF NOT EXISTS idx_broadcast_sends_dedup
    ON broadcast_sends(user_id, sent_at DESC)
    WHERE status = 'sent';

COMMENT ON TABLE broadcast_sends IS
    'Per-recipient delivery log with open/click tracking.';
