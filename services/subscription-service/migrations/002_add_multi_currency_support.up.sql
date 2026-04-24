-- Добавляем поддержку нескольких валют для тарифов

-- Переименовываем price в price_rub для ясности
ALTER TABLE device_addon_pricing 
    RENAME COLUMN price TO price_rub;

-- Добавляем price_stars для Telegram Stars
ALTER TABLE device_addon_pricing 
    ADD COLUMN IF NOT EXISTS price_stars INTEGER DEFAULT 0;

-- Обновляем существующие записи (примерный курс: 1 RUB = 1 Star)
-- Можно настроить курс по-другому
UPDATE device_addon_pricing 
SET price_stars = ROUND(price_rub)::INTEGER 
WHERE price_stars = 0;

-- Комментарии
COMMENT ON COLUMN device_addon_pricing.price_rub IS 'Цена в рублях (для YooMoney/ЮKassa)';
COMMENT ON COLUMN device_addon_pricing.price_stars IS 'Цена в Telegram Stars';

-- Обновляем base_price в subscription_plans
ALTER TABLE subscription_plans 
    RENAME COLUMN base_price TO base_price_rub;

ALTER TABLE subscription_plans 
    ADD COLUMN IF NOT EXISTS base_price_stars INTEGER DEFAULT 0;

UPDATE subscription_plans 
SET base_price_stars = ROUND(base_price_rub)::INTEGER 
WHERE base_price_stars = 0;

COMMENT ON COLUMN subscription_plans.base_price_rub IS 'Базовая цена в рублях';
COMMENT ON COLUMN subscription_plans.base_price_stars IS 'Базовая цена в Telegram Stars';
