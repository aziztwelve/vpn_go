-- Rollback: возвращаем _rub суффиксы и stars-колонки.

ALTER TABLE device_addon_pricing RENAME COLUMN price     TO price_rub;
ALTER TABLE subscription_plans   RENAME COLUMN base_price TO base_price_rub;

ALTER TABLE device_addon_pricing
    ADD COLUMN IF NOT EXISTS price_stars INTEGER NOT NULL DEFAULT 0 CHECK (price_stars >= 0);
ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS base_price_stars INTEGER DEFAULT 0;
ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS price_stars INTEGER NOT NULL DEFAULT 0 CHECK (price_stars >= 0);

-- Восстанавливаем предыдущие seed'ы (из 005_add_stars_price).
UPDATE subscription_plans SET price_stars = 100 WHERE id = 1;
UPDATE subscription_plans SET price_stars = 250 WHERE id = 2;
UPDATE subscription_plans SET price_stars = 450 WHERE id = 3;
UPDATE subscription_plans SET price_stars = 800 WHERE id = 4;
UPDATE subscription_plans SET base_price_stars = ROUND(base_price_rub)::INTEGER WHERE base_price_stars = 0;

UPDATE device_addon_pricing SET price_stars = ROUND(price_rub * 0.5)::INTEGER WHERE price_stars = 0;

DROP TABLE IF EXISTS currency_rates;
