UPDATE subscription_plans SET base_price = 1.00 WHERE id = 100;
UPDATE device_addon_pricing SET price = 1.00 WHERE plan_id = 100 AND max_devices = 2;
