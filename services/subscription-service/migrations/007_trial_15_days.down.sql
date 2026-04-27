-- Откат: возвращаем пробный период обратно 3 дня.
UPDATE subscription_plans
SET duration_days = 3
WHERE id = 99 AND is_trial = true;
