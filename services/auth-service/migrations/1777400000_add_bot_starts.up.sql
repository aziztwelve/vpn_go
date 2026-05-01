-- bot_starts — воронка "нажал /start в боте → открыл Mini App".
--
-- Записывается gateway'ем в момент handleStart (ON CONFLICT DO NOTHING —
-- фиксируем ТОЛЬКО первое нажатие; повторные /start не сдвигают started_at).
-- Поле opened_app_at проставляется auth-service'ом в ValidateTelegramUser
-- при создании нового users-record (=первый open Mini App).
--
-- Воронка:
--   SELECT
--     COUNT(*) AS total_starts,
--     COUNT(opened_app_at) AS opened_app,
--     COUNT(*) - COUNT(opened_app_at) AS bounced,
--     ROUND(100.0 * COUNT(opened_app_at) / NULLIF(COUNT(*), 0), 1) AS conv_pct
--   FROM bot_starts;
--
-- Backfill для существующих юзеров НЕ делаем — считаем воронку только с
-- момента включения трекинга.

CREATE TABLE IF NOT EXISTS bot_starts (
    telegram_id   BIGINT      PRIMARY KEY,
    username      VARCHAR(64) NOT NULL DEFAULT '',
    first_name    VARCHAR(255) NOT NULL DEFAULT '',
    start_param   VARCHAR(64) NOT NULL DEFAULT '',
    started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    opened_app_at TIMESTAMPTZ
);

CREATE INDEX idx_bot_starts_started_at ON bot_starts(started_at);
CREATE INDEX idx_bot_starts_opened_app_at ON bot_starts(opened_app_at) WHERE opened_app_at IS NULL;

COMMENT ON TABLE  bot_starts                IS 'Воронка /start в боте → open Mini App';
COMMENT ON COLUMN bot_starts.start_param    IS 'Параметр /start (ref_xxx или пусто)';
COMMENT ON COLUMN bot_starts.opened_app_at  IS 'NULL = нажал /start, но не открыл Mini App';
