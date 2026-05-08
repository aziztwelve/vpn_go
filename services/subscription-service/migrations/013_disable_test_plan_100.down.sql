-- 013 down: возвращаем тест-план id=100 в видимость.
-- device_addon_pricing восстанавливаем с ценой 3₽ за 2 устройства (как было
-- после миграции 009).
UPDATE subscription_plans SET is_active = true WHERE id = 100;
INSERT INTO device_addon_pricing (plan_id, max_devices, price)
VALUES (100, 2, 3.00)
ON CONFLICT (plan_id, max_devices) DO UPDATE SET price = EXCLUDED.price;
