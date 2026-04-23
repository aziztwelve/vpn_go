-- Откат: возвращаем триалу max_devices=1. Работает только если в БД не
-- осталось активных trial-подписок с max_devices=2 — старые подписки
-- (subscriptions.max_devices) не трогаем, только сам план.
UPDATE subscription_plans
SET max_devices = 1
WHERE is_trial = true AND max_devices = 2;
