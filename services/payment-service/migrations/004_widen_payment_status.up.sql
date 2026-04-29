-- Расширяем колонку payments.status: 'paid_subscription_done' = 22 символа,
-- не помещается в varchar(20). Это исправление к миграции 003 — без него
-- INSERT/UPDATE с новым промежуточным статусом падает с
--   ERROR: value too long for type character varying(20)
-- хотя CHECK constraint его уже разрешает.
ALTER TABLE payments ALTER COLUMN status TYPE VARCHAR(32);
