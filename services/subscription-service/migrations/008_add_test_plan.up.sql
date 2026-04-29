-- 008: тестовый план 1₽ / 1 мес. id=100 — за рамками обычной 1-4 нумерации
-- и триального 99. Видимость в /api/v1/subscriptions/plans гейтится в
-- gateway-handler по user_id=13 (aziztwelve) — захардкожено.
INSERT INTO subscription_plans (id, name, duration_days, max_devices, base_price, is_active, is_trial)
VALUES (100, 'Тест 1₽', 30, 2, 1.00, true, false)
ON CONFLICT (id) DO NOTHING;

INSERT INTO device_addon_pricing (plan_id, max_devices, price)
VALUES (100, 2, 1.00)
ON CONFLICT (plan_id, max_devices) DO NOTHING;
