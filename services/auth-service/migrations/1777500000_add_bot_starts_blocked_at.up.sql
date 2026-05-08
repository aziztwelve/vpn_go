-- blocked_at — момент, когда юзер заблокировал бот (или был помечен как
-- заблокировавший в ходе активной проверки sendChatAction).
--
-- Источники заполнения:
--   1. Периодический аудит sendChatAction → парсим 403 "bot was blocked
--      by the user" и ставим blocked_at = NOW().
--   2. (TODO) Webhook my_chat_member при new_chat_member.status='kicked'.
--   3. (TODO) Ошибка 403 при sendMessage из notifier'ов — там же ставим
--      blocked_at, чтобы не дёргать API повторно.
--
-- NULL = юзер не заблокировал бот (на момент последней проверки).

ALTER TABLE bot_starts
    ADD COLUMN IF NOT EXISTS blocked_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_bot_starts_blocked_at
    ON bot_starts(blocked_at) WHERE blocked_at IS NOT NULL;

COMMENT ON COLUMN bot_starts.blocked_at IS
    'Момент, когда юзер заблокировал бот (NULL = не заблокировал)';
