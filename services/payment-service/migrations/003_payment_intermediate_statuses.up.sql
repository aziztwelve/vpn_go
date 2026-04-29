-- Расширяем CHECK constraint payments.status промежуточными статусами
-- для пошаговой идемпотентности webhook'а оплаты:
--
--   pending                  → создан инвойс, ждём оплату
--   paid_db_only             → MarkPaid сделан, подписка ещё не создана
--   paid_subscription_done   → подписка создана, VPN-юзер ещё не зарегистрирован
--   paid                     → ВСЁ ПРОШЛО (финальный успех)
--   cancelled / failed       → не оплачено
--   refunded                 → возврат
--
-- Зачем: сейчас если webhook падает между шагами (например, упал
-- subscription-service после того как мы пометили payment.paid) — повторный
-- webhook не докатит, потому что check `if status == paid → skip` отдаёт early
-- return. Промежуточные статусы позволяют каждому ретраю продолжить с того
-- шага, где остановились. См. docs/services/payment-integration.md.

-- ВАЖНО: 'paid_subscription_done' = 22 символа, текущая колонка status это varchar(20).
-- Расширение колонки сделано отдельной миграцией 004_widen_payment_status.up.sql
-- (применяется ПОСЛЕ этой). Эта миграция изменена постфактум через 004 — не
-- редактируй её содержимое если 003 уже применена в проде.
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_status_check;

ALTER TABLE payments
    ADD CONSTRAINT payments_status_check
    CHECK (status IN (
        'pending',
        'paid_db_only',
        'paid_subscription_done',
        'paid',
        'failed',
        'refunded',
        'cancelled'
    ));
