-- Откат 010 к состоянию проды на момент применения 010 (тот ручной
-- прайс, который был в БД минуя миграции — до 130-269-449 и т.д.).
-- Триал → 15 дней (как было после 007).

UPDATE subscription_plans SET duration_days = 15 WHERE id = 99 AND is_trial = true;

-- Прайс возвращаем на ручной prod-снимок.
INSERT INTO device_addon_pricing (plan_id, max_devices, price) VALUES
    (1, 2,  130.00),
    (1, 3,  189.00),
    (1, 4,  229.00),
    (1, 5,  269.00),
    (1, 10, 449.00),
    (2, 2,  329.00),
    (2, 3,  449.00),
    (2, 4,  549.00),
    (2, 5,  659.00),
    (2, 10, 1099.00),
    (3, 2,  599.00),
    (3, 3,  799.00),
    (3, 4,  999.00),
    (3, 5,  1199.00),
    (3, 10, 1999.00),
    (4, 2,  1099.00),
    (4, 3,  1499.00),
    (4, 4,  1799.00),
    (4, 5,  2099.00),
    (4, 10, 3799.00)
ON CONFLICT (plan_id, max_devices) DO UPDATE SET price = EXCLUDED.price;

UPDATE subscription_plans SET base_price = 130.00  WHERE id = 1;
UPDATE subscription_plans SET base_price = 329.00  WHERE id = 2;
UPDATE subscription_plans SET base_price = 599.00  WHERE id = 3;
UPDATE subscription_plans SET base_price = 1099.00 WHERE id = 4;
