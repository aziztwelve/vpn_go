-- Добавляем поля для поддержки нескольких провайдеров платежей

-- Добавляем amount_rub для YooMoney/ЮKassa
ALTER TABLE payments 
    ADD COLUMN IF NOT EXISTS amount_rub DECIMAL(10,2) DEFAULT 0;

-- Добавляем currency (XTR для Stars, RUB для рублей)
ALTER TABLE payments 
    ADD COLUMN IF NOT EXISTS currency VARCHAR(3) DEFAULT 'XTR';

-- Добавляем статус cancelled
ALTER TABLE payments 
    DROP CONSTRAINT IF EXISTS payments_status_check;

ALTER TABLE payments 
    ADD CONSTRAINT payments_status_check 
    CHECK (status IN ('pending', 'paid', 'failed', 'refunded', 'cancelled'));

-- Обновляем существующие записи
UPDATE payments 
SET currency = 'XTR' 
WHERE provider = 'telegram_stars' AND currency IS NULL;

-- Комментарии
COMMENT ON COLUMN payments.amount_stars IS 'Сумма в Telegram Stars (для telegram_stars провайдера)';
COMMENT ON COLUMN payments.amount_rub IS 'Сумма в рублях (для yoomoney/yookassa провайдеров)';
COMMENT ON COLUMN payments.currency IS 'Валюта платежа: XTR (Stars), RUB, USD';
COMMENT ON COLUMN payments.provider IS 'Провайдер платежа: telegram_stars, yoomoney, yookassa';
