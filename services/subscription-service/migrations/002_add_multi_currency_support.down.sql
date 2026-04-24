-- Откат изменений для поддержки нескольких валют

-- Удаляем price_stars
ALTER TABLE device_addon_pricing 
    DROP COLUMN IF EXISTS price_stars;

-- Возвращаем старое имя price
ALTER TABLE device_addon_pricing 
    RENAME COLUMN price_rub TO price;

-- Удаляем base_price_stars
ALTER TABLE subscription_plans 
    DROP COLUMN IF EXISTS base_price_stars;

-- Возвращаем старое имя base_price
ALTER TABLE subscription_plans 
    RENAME COLUMN base_price_rub TO base_price;
