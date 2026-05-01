-- Откат 011: убираем тиры 6-9 устройств для всех планов.
DELETE FROM device_addon_pricing
WHERE plan_id IN (1, 2, 3, 4)
  AND max_devices BETWEEN 6 AND 9;
