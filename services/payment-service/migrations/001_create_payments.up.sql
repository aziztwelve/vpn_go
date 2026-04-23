-- payments: история оплат Telegram Stars.
--
-- Идемпотентность: UNIQUE(external_id) + ON CONFLICT DO NOTHING
-- в payment-service.HandleSuccessfulPayment. Telegram может ретраить
-- webhook до 30 минут при 5xx ответе.
CREATE TABLE IF NOT EXISTS payments (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    plan_id INTEGER NOT NULL REFERENCES subscription_plans(id) ON DELETE RESTRICT,
    max_devices INTEGER NOT NULL,
    amount_stars INTEGER NOT NULL CHECK (amount_stars > 0),

    -- pending → paid | failed | refunded
    status VARCHAR(20) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','paid','failed','refunded')),

    -- telegram_payment_charge_id для paid; NULL для pending
    external_id VARCHAR(255) UNIQUE,

    provider VARCHAR(50) NOT NULL DEFAULT 'telegram_stars',

    -- Любая дополнительная метадата (invoice_payload и т.п.)
    metadata JSONB,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    paid_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_payments_user_id ON payments(user_id);
CREATE INDEX IF NOT EXISTS idx_payments_status ON payments(status);
CREATE INDEX IF NOT EXISTS idx_payments_created_at ON payments(created_at DESC);
