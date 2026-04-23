-- backfill-trial.sql
-- Выдаёт пробный период (plan_id=99, 3 дня) всем существующим юзерам
-- у которых ещё не выдан триал (trial_used_at IS NULL) и нет активной подписки.
--
-- Запускается вручную ОДИН раз после деплоя 05-trial-period миграций:
--   docker exec -i vpn-postgres psql -U vpn -d vpn < deploy/scripts/backfill-trial.sql
--
-- Идемпотентный: повторный запуск не создаст дубликатов (WHERE trial_used_at IS NULL).
-- Не создаёт записи в vpn_users — это сделает cron/следующий /auth/validate,
-- либо можно прогнать руками:
--   for uid in $(psql -c "SELECT id FROM users WHERE trial_used_at=NOW()"); do
--     grpcurl -plaintext -d "{\"user_id\": $uid, \"subscription_id\": ...}" ...
--   done
-- Проще: юзер при следующем заходе в Mini App получит VPN при первом GET /vpn/servers/{id}/link.

BEGIN;

-- 1. Создать trial-подписки для всех юзеров без подписок + без использованного триала.
WITH trial_plan AS (
    SELECT id, duration_days, max_devices FROM subscription_plans WHERE is_trial = true LIMIT 1
),
eligible_users AS (
    SELECT u.id AS user_id
    FROM users u
    LEFT JOIN subscriptions s ON s.user_id = u.id AND s.status IN ('active', 'trial') AND s.expires_at > NOW()
    WHERE u.trial_used_at IS NULL
      AND u.is_banned = false
      AND s.id IS NULL
),
inserted_subs AS (
    INSERT INTO subscriptions (user_id, plan_id, max_devices, total_price, started_at, expires_at, status)
    SELECT e.user_id, tp.id, tp.max_devices, 0, NOW(), NOW() + INTERVAL '1 day' * tp.duration_days, 'trial'
    FROM eligible_users e, trial_plan tp
    RETURNING user_id
)
UPDATE users SET trial_used_at = NOW()
WHERE id IN (SELECT user_id FROM inserted_subs);

-- 2. Диагностика: показать что сделали.
SELECT
    (SELECT COUNT(*) FROM users WHERE trial_used_at IS NOT NULL) AS users_with_trial_used,
    (SELECT COUNT(*) FROM subscriptions WHERE status = 'trial') AS active_trials;

COMMIT;
