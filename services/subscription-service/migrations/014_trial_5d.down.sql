-- Откат миграции 014: возврат к 3 дням (как было после 010).
UPDATE subscription_plans
SET duration_days = 3
WHERE id = 99 AND is_trial = true;
