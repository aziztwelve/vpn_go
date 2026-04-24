-- ============================================================
-- 006: RUB = single source of truth for prices
-- ============================================================
-- Отходим от схемы "две параллельные цены (RUB + Stars)". Теперь:
--   • subscription_plans.base_price         — цена в рублях (primary)
--   • device_addon_pricing.price            — цена в рублях (primary)
--   • currency_rates                        — курсы для конвертации
--   • Stars (и будущие валюты) считаются на лету: ceil(rub / rate_to_rub)
--
-- Это убирает двойную точку истины (раньше price_stars мог разойтись
-- с price_rub при обновлении) и делает систему расширяемой — чтобы
-- добавить USD/EUR/crypto, достаточно INSERT в currency_rates.
-- ============================================================

-- 1) Таблица курсов.
CREATE TABLE IF NOT EXISTS currency_rates (
    currency     VARCHAR(16)    PRIMARY KEY,
    rate_to_rub  NUMERIC(12, 6) NOT NULL CHECK (rate_to_rub > 0),
    description  TEXT,
    updated_at   TIMESTAMPTZ    NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE  currency_rates             IS 'Курс валюты → рубль. 1 unit = <rate_to_rub> RUB.';
COMMENT ON COLUMN currency_rates.rate_to_rub IS 'Сколько рублей в одной единице этой валюты.';

-- 2) Seed: текущий implicit-курс — 2 RUB / 1 Star (по существующим планам).
--    199₽ / 100⭐ = 1.99 ≈ 2.00.
INSERT INTO currency_rates (currency, rate_to_rub, description) VALUES
    ('STARS', 2.00, 'Telegram Stars (in-app currency)')
ON CONFLICT (currency) DO NOTHING;

-- 3) Дропаем дублирующие Stars-колонки — теперь считаются из RUB * rate.
ALTER TABLE subscription_plans   DROP COLUMN IF EXISTS price_stars;
ALTER TABLE subscription_plans   DROP COLUMN IF EXISTS base_price_stars;
ALTER TABLE device_addon_pricing DROP COLUMN IF EXISTS price_stars;

-- 4) Переименовываем _rub-колонки обратно в короткие имена.
--    Раз RUB = primary, суффикс избыточен (как в субд bitcoind нет суффикса
--    _btc у balance). Появится USD — добавим balance_usd рядом с balance.
ALTER TABLE subscription_plans   RENAME COLUMN base_price_rub TO base_price;
ALTER TABLE device_addon_pricing RENAME COLUMN price_rub     TO price;
