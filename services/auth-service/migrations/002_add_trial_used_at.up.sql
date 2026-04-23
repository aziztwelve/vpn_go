-- trial_used_at — один раз в жизни юзера (по telegram_id) мы выдаём
-- пробный период. Фиксируем факт выдачи прямо в users, чтобы subscription-service
-- при StartTrial мог атомарно проверить через SELECT ... FOR UPDATE.
-- Schema ownership: users — это auth-домен, поэтому колонка живёт тут.
ALTER TABLE users ADD COLUMN IF NOT EXISTS trial_used_at TIMESTAMPTZ;
