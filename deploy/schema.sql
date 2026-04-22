-- ============================================================
-- ExtraVPN Database Schema (PostgreSQL 15+) - Xray VLESS
-- Spec-Driven Development: Version 2.0
-- ============================================================

-- 1. Пользователи (из Telegram)
CREATE TABLE users (
    id BIGSERIAL PRIMARY KEY,
    telegram_id BIGINT NOT NULL UNIQUE,
    username VARCHAR(255),
    first_name VARCHAR(255),
    last_name VARCHAR(255),
    photo_url TEXT,
    language_code VARCHAR(10) DEFAULT 'ru',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at TIMESTAMPTZ,
    is_banned BOOLEAN NOT NULL DEFAULT FALSE,
    role VARCHAR(50) NOT NULL DEFAULT 'user', -- user, admin, partner
    balance DECIMAL(12,2) NOT NULL DEFAULT 0
);

COMMENT ON TABLE users IS 'Пользователи сервиса (авторизация через Telegram)';
COMMENT ON COLUMN users.telegram_id IS 'Telegram User ID для валидации initData';
COMMENT ON COLUMN users.role IS 'user, admin, partner (партнёр с 30% отчислений)';
COMMENT ON COLUMN users.balance IS 'Баланс для выплат по партнёрской программе';

CREATE INDEX idx_users_role ON users(role);
CREATE INDEX idx_users_telegram_id ON users(telegram_id);

-- 2. VPN Серверы
CREATE TABLE vpn_servers (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    country_code VARCHAR(2) NOT NULL, -- RU, DE, US, SG
    city VARCHAR(100),
    host VARCHAR(255) NOT NULL,
    port INT NOT NULL DEFAULT 443,
    public_key VARCHAR(100) NOT NULL, -- Reality public key
    private_key VARCHAR(100) NOT NULL, -- Reality private key (для конфига)
    short_id VARCHAR(50) NOT NULL,
    dest VARCHAR(255) NOT NULL DEFAULT 'github.com:443',
    server_names JSONB NOT NULL DEFAULT '["github.com", "www.github.com"]',
    api_host VARCHAR(255) NOT NULL DEFAULT '127.0.0.1',
    api_port INT NOT NULL DEFAULT 10085,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    max_users INT NOT NULL DEFAULT 1000,
    current_users INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE vpn_servers IS 'VPN серверы в разных локациях';
COMMENT ON COLUMN vpn_servers.public_key IS 'Reality public key для клиентов';
COMMENT ON COLUMN vpn_servers.private_key IS 'Reality private key для сервера';
COMMENT ON COLUMN vpn_servers.short_id IS 'Reality short ID';
COMMENT ON COLUMN vpn_servers.dest IS 'Reality destination (маскировка)';
COMMENT ON COLUMN vpn_servers.server_names IS 'SNI для Reality';
COMMENT ON COLUMN vpn_servers.api_host IS 'Xray API host (обычно 127.0.0.1)';
COMMENT ON COLUMN vpn_servers.api_port IS 'Xray API port (обычно 10085)';

CREATE INDEX idx_vpn_servers_active ON vpn_servers(is_active);
CREATE INDEX idx_vpn_servers_country ON vpn_servers(country_code);

-- 3. Тарифные планы (базовые)
CREATE TABLE subscription_plans (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    duration_days INT NOT NULL,
    max_devices INT NOT NULL DEFAULT 2, -- Максимальное количество одновременных подключений
    base_price DECIMAL(10,2) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE
);

COMMENT ON TABLE subscription_plans IS 'Базовые тарифные планы (1/3/6/12 месяцев)';
COMMENT ON COLUMN subscription_plans.max_devices IS 'Максимальное количество одновременных подключений';
COMMENT ON COLUMN subscription_plans.base_price IS 'Цена в рублях (например, 199₽ за 1 месяц)';

-- Предзаполнение тарифов
INSERT INTO subscription_plans (name, duration_days, max_devices, base_price) VALUES
('1 месяц', 30, 2, 199),
('3 месяца', 90, 2, 550),
('6 месяцев', 180, 2, 1100),
('12 месяцев', 365, 2, 1999);

-- 4. Цены на дополнительные устройства для каждого тарифа
CREATE TABLE device_addon_pricing (
    id SERIAL PRIMARY KEY,
    plan_id INT NOT NULL REFERENCES subscription_plans(id) ON DELETE CASCADE,
    max_devices INT NOT NULL, -- Общее количество устройств
    price DECIMAL(10,2) NOT NULL,
    UNIQUE(plan_id, max_devices)
);

COMMENT ON TABLE device_addon_pricing IS 'Полная стоимость подписки при выборе N устройств';
COMMENT ON COLUMN device_addon_pricing.max_devices IS 'Максимальное количество одновременных подключений (от 3 до 99)';
COMMENT ON COLUMN device_addon_pricing.price IS 'Итоговая цена в рублях (289₽ за 3 устройства на 1 месяц)';

-- Пример данных для тарифа "1 месяц"
INSERT INTO device_addon_pricing (plan_id, max_devices, price) VALUES
(1, 3, 289),
(1, 4, 379),
(1, 5, 469),
(1, 6, 559),
(1, 7, 649);

-- 5. Подписки пользователей
CREATE TABLE subscriptions (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id INT NOT NULL REFERENCES subscription_plans(id),
    max_devices INT NOT NULL, -- Выбранное пользователем количество устройств
    total_price DECIMAL(10,2) NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active', -- active, expired, cancelled
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE subscriptions IS 'Активные и исторические подписки пользователей';
COMMENT ON COLUMN subscriptions.max_devices IS 'Максимальное количество одновременных подключений';
COMMENT ON COLUMN subscriptions.expires_at IS 'Дата окончания подписки (с учётом бонусных дней)';

CREATE INDEX idx_subscriptions_user_id ON subscriptions(user_id);
CREATE INDEX idx_subscriptions_expires_at ON subscriptions(expires_at);
CREATE INDEX idx_subscriptions_status ON subscriptions(status);

-- 6. VPN пользователи (один UUID на все серверы)
CREATE TABLE vpn_users (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subscription_id BIGINT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    uuid UUID NOT NULL UNIQUE,
    email VARCHAR(255) NOT NULL UNIQUE, -- Уникальный email для Xray
    flow VARCHAR(50) NOT NULL DEFAULT 'xtls-rprx-vision',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen TIMESTAMPTZ
);

COMMENT ON TABLE vpn_users IS 'VPN пользователи с UUID (один UUID на все серверы)';
COMMENT ON COLUMN vpn_users.uuid IS 'UUID для подключения к Xray (один на все серверы)';
COMMENT ON COLUMN vpn_users.email IS 'Email идентификатор в Xray (формат: user_{user_id}_{subscription_id})';
COMMENT ON COLUMN vpn_users.flow IS 'Xray flow (xtls-rprx-vision)';

CREATE INDEX idx_vpn_users_user_id ON vpn_users(user_id);
CREATE INDEX idx_vpn_users_subscription_id ON vpn_users(subscription_id);
CREATE INDEX idx_vpn_users_uuid ON vpn_users(uuid);

-- 7. Активные подключения (для контроля лимита устройств)
CREATE TABLE active_connections (
    id BIGSERIAL PRIMARY KEY,
    vpn_user_id BIGINT NOT NULL REFERENCES vpn_users(id) ON DELETE CASCADE,
    server_id INT NOT NULL REFERENCES vpn_servers(id),
    device_identifier VARCHAR(255), -- IP или другой идентификатор устройства
    connected_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(vpn_user_id, server_id, device_identifier)
);

COMMENT ON TABLE active_connections IS 'Активные подключения для контроля лимита устройств';
COMMENT ON COLUMN active_connections.device_identifier IS 'IP адрес или другой идентификатор устройства';
COMMENT ON COLUMN active_connections.last_seen IS 'Последняя активность (обновляется каждые 5 минут)';

CREATE INDEX idx_active_connections_vpn_user_id ON active_connections(vpn_user_id);
CREATE INDEX idx_active_connections_last_seen ON active_connections(last_seen);

-- 8. Логи трафика (агрегируются для биллинга)
CREATE TABLE traffic_logs (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    vpn_user_id BIGINT NOT NULL REFERENCES vpn_users(id) ON DELETE CASCADE,
    server_id INT NOT NULL REFERENCES vpn_servers(id),
    bytes_rx BIGINT NOT NULL DEFAULT 0,
    bytes_tx BIGINT NOT NULL DEFAULT 0,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE traffic_logs IS 'Учёт входящего и исходящего трафика';
COMMENT ON COLUMN traffic_logs.bytes_rx IS 'Принято байт (downlink)';
COMMENT ON COLUMN traffic_logs.bytes_tx IS 'Отправлено байт (uplink)';

CREATE INDEX idx_traffic_logs_user_id ON traffic_logs(user_id);
CREATE INDEX idx_traffic_logs_vpn_user_id ON traffic_logs(vpn_user_id);
CREATE INDEX idx_traffic_logs_server_id ON traffic_logs(server_id);
CREATE INDEX idx_traffic_logs_recorded_at ON traffic_logs(recorded_at);

-- 9. Платежи
CREATE TABLE payments (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subscription_id BIGINT REFERENCES subscriptions(id),
    amount DECIMAL(10,2) NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'RUB',
    provider VARCHAR(50) NOT NULL, -- yookassa, stripe, crypto
    provider_payment_id VARCHAR(255),
    status VARCHAR(20) NOT NULL DEFAULT 'pending', -- pending, completed, failed, refunded
    metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);

COMMENT ON TABLE payments IS 'История платежей пользователей';
COMMENT ON COLUMN payments.provider IS 'Платёжная система (yookassa, stripe, crypto)';
COMMENT ON COLUMN payments.provider_payment_id IS 'ID платежа в системе провайдера';
COMMENT ON COLUMN payments.metadata IS 'JSON с дополнительными данными';

CREATE INDEX idx_payments_user_id ON payments(user_id);
CREATE INDEX idx_payments_status ON payments(status);
CREATE INDEX idx_payments_provider_payment_id ON payments(provider_payment_id);

-- 10. Реферальные ссылки пользователей
CREATE TABLE referral_links (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token VARCHAR(50) NOT NULL UNIQUE,
    clicks INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE referral_links IS 'Уникальные реферальные ссылки пользователей';

CREATE INDEX idx_referral_links_token ON referral_links(token);

-- 11. Связи "пригласитель-приглашённый"
CREATE TABLE referral_relationships (
    inviter_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invited_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(20) NOT NULL DEFAULT 'registered',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (inviter_id, invited_id)
);

COMMENT ON TABLE referral_relationships IS 'Кто кого пригласил';
COMMENT ON COLUMN referral_relationships.status IS 'registered - зарегистрировался, purchased - совершил покупку';

CREATE INDEX idx_referral_relationships_inviter ON referral_relationships(inviter_id);
CREATE INDEX idx_referral_relationships_invited ON referral_relationships(invited_id);

-- 12. Начисление бонусов за рефералов
CREATE TABLE referral_bonuses (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invited_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    bonus_type VARCHAR(20) NOT NULL, -- 'days', 'balance'
    days_amount INT,
    balance_amount DECIMAL(12,2),
    is_applied BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE referral_bonuses IS 'Бонусы, полученные пользователем по реферальной программе';
COMMENT ON COLUMN referral_bonuses.user_id IS 'Кому начислен бонус (пригласителю)';
COMMENT ON COLUMN referral_bonuses.invited_user_id IS 'Кто принёс бонус (приглашённый)';
COMMENT ON COLUMN referral_bonuses.bonus_type IS 'days - продление подписки, balance - пополнение баланса партнёра';

CREATE INDEX idx_referral_bonuses_user_id ON referral_bonuses(user_id);
CREATE INDEX idx_referral_bonuses_is_applied ON referral_bonuses(is_applied);

-- 13. Заявки на вывод средств (для партнёров)
CREATE TABLE withdrawal_requests (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    amount DECIMAL(10,2) NOT NULL,
    payment_method VARCHAR(50) NOT NULL,
    payment_details JSONB NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    admin_comment TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ
);

COMMENT ON TABLE withdrawal_requests IS 'Заявки партнёров на вывод средств';

CREATE INDEX idx_withdrawal_requests_user_id ON withdrawal_requests(user_id);
CREATE INDEX idx_withdrawal_requests_status ON withdrawal_requests(status);

-- ============================================================
-- Триггеры
-- ============================================================

CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ language 'plpgsql';

CREATE TRIGGER update_users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- Автоматическая очистка старых подключений (> 10 минут без активности)
CREATE OR REPLACE FUNCTION cleanup_stale_connections()
RETURNS void AS $$
BEGIN
    DELETE FROM active_connections 
    WHERE last_seen < NOW() - INTERVAL '10 minutes';
END;
$$ language 'plpgsql';
