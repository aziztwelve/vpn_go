DROP INDEX IF EXISTS idx_bot_starts_blocked_at;
ALTER TABLE bot_starts DROP COLUMN IF EXISTS blocked_at;
