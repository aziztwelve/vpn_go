-- 012: промо-план id=101 «Промо 79₽ / 1 мес» — точечная скидка для
-- юзеров, у которых истёк триал на момент когда оплата была закрыта
-- (whitelist через @aziztwelve / @mans_lll). Раздаётся ОДНОРАЗОВЫМИ
-- токенами через bot-команду `/promo @username` (см. auth-service
-- migration 1777800000_add_promo_codes.up.sql).
--
-- НИКОГДА не показывается в публичном /api/v1/subscriptions/plans —
-- видимость гейтится в gateway/internal/handler/subscription.go (план
-- id=101 не пускаем в общий список вообще, только через /promo/p/{token}).
--
-- Цена 79₽ vs обычные 199₽ — скидка 60% «извинение за задержку платежей».
INSERT INTO subscription_plans (id, name, duration_days, max_devices, base_price, is_active, is_trial)
VALUES (101, 'Промо: 1 месяц со скидкой', 30, 2, 79.00, true, false)
ON CONFLICT (id) DO NOTHING;

INSERT INTO device_addon_pricing (plan_id, max_devices, price)
VALUES (101, 2, 79.00)
ON CONFLICT (plan_id, max_devices) DO NOTHING;
