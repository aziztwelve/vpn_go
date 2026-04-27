-- Увеличиваем пробный период с 3 до 15 дней.
UPDATE subscription_plans
SET duration_days = 15
WHERE id = 99 AND is_trial = true;
