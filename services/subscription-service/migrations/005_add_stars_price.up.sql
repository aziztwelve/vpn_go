-- Добавляем цены в Telegram Stars для каждой комбинации (план, устройства).
-- До оплаты — рубли. Сейчас — Stars (выбрано в Этапе 5 MVP).
ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS price_stars INTEGER NOT NULL DEFAULT 0 CHECK (price_stars >= 0);

ALTER TABLE device_addon_pricing
    ADD COLUMN IF NOT EXISTS price_stars INTEGER NOT NULL DEFAULT 0 CHECK (price_stars >= 0);

-- Базовые цены в Stars (для справки при показе плана до выбора кол-ва устройств).
UPDATE subscription_plans SET price_stars = 100 WHERE id = 1;   -- 1 мес
UPDATE subscription_plans SET price_stars = 250 WHERE id = 2;   -- 3 мес
UPDATE subscription_plans SET price_stars = 450 WHERE id = 3;   -- 6 мес
UPDATE subscription_plans SET price_stars = 800 WHERE id = 4;   -- 12 мес

-- Реальные цены за полный пакет (план × кол-во устройств).
-- Формула: longer-plan — скидка, больше устройств — скидка на устройство.
UPDATE device_addon_pricing SET price_stars = 100 WHERE plan_id = 1 AND max_devices = 2;
UPDATE device_addon_pricing SET price_stars = 200 WHERE plan_id = 1 AND max_devices = 5;
UPDATE device_addon_pricing SET price_stars = 350 WHERE plan_id = 1 AND max_devices = 10;

UPDATE device_addon_pricing SET price_stars = 250 WHERE plan_id = 2 AND max_devices = 2;
UPDATE device_addon_pricing SET price_stars = 500 WHERE plan_id = 2 AND max_devices = 5;
UPDATE device_addon_pricing SET price_stars = 900 WHERE plan_id = 2 AND max_devices = 10;

UPDATE device_addon_pricing SET price_stars = 450 WHERE plan_id = 3 AND max_devices = 2;
UPDATE device_addon_pricing SET price_stars = 900 WHERE plan_id = 3 AND max_devices = 5;
UPDATE device_addon_pricing SET price_stars = 1600 WHERE plan_id = 3 AND max_devices = 10;

UPDATE device_addon_pricing SET price_stars = 800 WHERE plan_id = 4 AND max_devices = 2;
UPDATE device_addon_pricing SET price_stars = 1600 WHERE plan_id = 4 AND max_devices = 5;
UPDATE device_addon_pricing SET price_stars = 2800 WHERE plan_id = 4 AND max_devices = 10;
