-- pending_bonus_days хранит начисленные бонусные дни, которые ещё не были
-- применены к подписке (нет активной подписки на момент начисления).
-- Списываются при следующем CreateSubscription/StartTrial и продлевают
-- expires_at на это количество дней.
ALTER TABLE users
ADD COLUMN IF NOT EXISTS pending_bonus_days INT NOT NULL DEFAULT 0;

COMMENT ON COLUMN users.pending_bonus_days IS 'Бонусные дни, ожидающие применения при следующей покупке/триале';
