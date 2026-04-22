-- Create subscription_plans table
CREATE TABLE IF NOT EXISTS subscription_plans (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    duration_days INTEGER NOT NULL,
    max_devices INTEGER NOT NULL DEFAULT 2,
    base_price DECIMAL(10,2) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Create device_addon_pricing table
CREATE TABLE IF NOT EXISTS device_addon_pricing (
    id SERIAL PRIMARY KEY,
    plan_id INTEGER NOT NULL REFERENCES subscription_plans(id) ON DELETE CASCADE,
    max_devices INTEGER NOT NULL,
    price DECIMAL(10,2) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(plan_id, max_devices)
);

-- Create subscriptions table
CREATE TABLE IF NOT EXISTS subscriptions (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id INTEGER NOT NULL REFERENCES subscription_plans(id),
    max_devices INTEGER NOT NULL,
    total_price DECIMAL(10,2) NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_subscriptions_user_id ON subscriptions(user_id);
CREATE INDEX idx_subscriptions_status ON subscriptions(status);
CREATE INDEX idx_subscriptions_expires_at ON subscriptions(expires_at);

-- Insert default plans
INSERT INTO subscription_plans (name, duration_days, max_devices, base_price) VALUES
    ('1 месяц', 30, 2, 199.00),
    ('3 месяца', 90, 2, 499.00),
    ('6 месяцев', 180, 2, 899.00),
    ('12 месяцев', 365, 2, 1599.00);

-- Insert device pricing for each plan
INSERT INTO device_addon_pricing (plan_id, max_devices, price) VALUES
    -- 1 month
    (1, 2, 199.00),
    (1, 5, 399.00),
    (1, 10, 699.00),
    -- 3 months
    (2, 2, 499.00),
    (2, 5, 999.00),
    (2, 10, 1799.00),
    -- 6 months
    (3, 2, 899.00),
    (3, 5, 1799.00),
    (3, 10, 3199.00),
    -- 12 months
    (4, 2, 1599.00),
    (4, 5, 3199.00),
    (4, 10, 5699.00);
