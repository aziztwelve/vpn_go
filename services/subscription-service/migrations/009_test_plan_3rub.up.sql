-- 009: повышаем тестовый план id=100 с 1₽ → 3₽ (тестируем минимальный
-- платёж побольше — у платеги/банков иногда отказ на сумме <2₽).
UPDATE subscription_plans SET base_price = 3.00 WHERE id = 100;
UPDATE device_addon_pricing SET price = 3.00 WHERE plan_id = 100 AND max_devices = 2;
