-- pending_referrals хранит реферальный токен, привязанный к telegram_id,
-- ДО того как юзер зарегистрировался в Mini App.
--
-- Flow:
--   1. Друг кликает https://t.me/maydavpnbot?start=ref_<token>
--   2. Бот получает /start ref_<token> от нового telegram_id
--   3. Бот вызывает SetPendingReferral(telegram_id, token) → upsert сюда
--   4. Юзер открывает Mini App → ValidateTelegramUser
--   5. Если юзер новый и ref_token из initData пустой —
--      auth-service "съедает" запись отсюда и регистрирует реферал.
--
-- Записи живут до явного use в ValidateTelegramUser (там же удаляются).
-- Старые висячие токены чистятся фоном по created_at (TTL ≈ 30 дней).
CREATE TABLE IF NOT EXISTS pending_referrals (
    telegram_id BIGINT PRIMARY KEY,
    ref_token VARCHAR(40) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pending_referrals_created_at ON pending_referrals(created_at);

COMMENT ON TABLE pending_referrals IS 'Реферальные токены, ожидающие первой регистрации юзера в Mini App (для ?start=ref_<token> deep-link)';
COMMENT ON COLUMN pending_referrals.telegram_id IS 'Telegram ID будущего юзера (до создания records в users)';
COMMENT ON COLUMN pending_referrals.ref_token IS 'Чистый токен (без префикса ref_)';
