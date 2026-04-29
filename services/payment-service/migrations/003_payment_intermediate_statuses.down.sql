-- Возвращаем старый CHECK без промежуточных статусов.
-- Перед откатом схлопываем зависшие промежуточные платежи в финальные:
-- считаем что если шаг 1 (MarkPaid) сделан — оплата по факту состоялась
-- (с точки зрения денег), маппим всё в 'paid'.
UPDATE payments
SET status = 'paid'
WHERE status IN ('paid_db_only', 'paid_subscription_done');

ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_status_check;

ALTER TABLE payments
    ADD CONSTRAINT payments_status_check
    CHECK (status IN ('pending', 'paid', 'failed', 'refunded', 'cancelled'));


