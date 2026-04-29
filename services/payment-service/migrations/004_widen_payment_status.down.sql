-- Возвращаем размер колонки до varchar(20). Миграционный инструмент
-- откатывает в обратном порядке: 004.down → 003.down, поэтому к моменту
-- этого ALTER TYPE в БД ещё могут лежать длинные значения 'paid_subscription_done'
-- (22 симв) — схлопываем их сами, чтобы ALTER не упал.
UPDATE payments
SET status = 'paid'
WHERE status IN ('paid_db_only', 'paid_subscription_done');

ALTER TABLE payments ALTER COLUMN status TYPE VARCHAR(20);
