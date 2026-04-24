-- Откат изменений для поддержки нескольких провайдеров

-- Удаляем новые поля
ALTER TABLE payments 
    DROP COLUMN IF EXISTS amount_rub;

ALTER TABLE payments 
    DROP COLUMN IF EXISTS currency;

-- Возвращаем старый constraint для status
ALTER TABLE payments 
    DROP CONSTRAINT IF EXISTS payments_status_check;

ALTER TABLE payments 
    ADD CONSTRAINT payments_status_check 
    CHECK (status IN ('pending', 'paid', 'failed', 'refunded'));
