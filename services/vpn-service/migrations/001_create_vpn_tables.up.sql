-- Create vpn_servers table
CREATE TABLE IF NOT EXISTS vpn_servers (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    location VARCHAR(255) NOT NULL,
    country_code VARCHAR(10) NOT NULL,
    host VARCHAR(255) NOT NULL,
    port INTEGER NOT NULL,
    public_key TEXT NOT NULL,
    private_key TEXT NOT NULL,
    short_id VARCHAR(255) NOT NULL,
    dest VARCHAR(255) NOT NULL DEFAULT 'github.com:443',
    server_names TEXT NOT NULL DEFAULT 'github.com',
    xray_api_host VARCHAR(255) NOT NULL DEFAULT 'localhost',
    xray_api_port INTEGER NOT NULL DEFAULT 10085,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    load_percent INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Create vpn_users table
CREATE TABLE IF NOT EXISTS vpn_users (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subscription_id BIGINT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    uuid VARCHAR(36) NOT NULL UNIQUE,
    email VARCHAR(255) NOT NULL,
    flow VARCHAR(50) NOT NULL DEFAULT 'xtls-rprx-vision',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, subscription_id)
);

-- Create active_connections table
CREATE TABLE IF NOT EXISTS active_connections (
    id BIGSERIAL PRIMARY KEY,
    vpn_user_id BIGINT NOT NULL REFERENCES vpn_users(id) ON DELETE CASCADE,
    server_id INTEGER NOT NULL REFERENCES vpn_servers(id) ON DELETE CASCADE,
    device_identifier VARCHAR(255) NOT NULL,
    connected_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(vpn_user_id, device_identifier)
);

CREATE INDEX idx_vpn_users_user_id ON vpn_users(user_id);
CREATE INDEX idx_vpn_users_uuid ON vpn_users(uuid);
CREATE INDEX idx_active_connections_vpn_user_id ON active_connections(vpn_user_id);
CREATE INDEX idx_active_connections_last_seen ON active_connections(last_seen);

-- Insert test servers
INSERT INTO vpn_servers (name, location, country_code, host, port, public_key, private_key, short_id) VALUES
    ('USA Server', 'New York', 'US', 'us.vpn.example.com', 443, 'test_public_key_us', 'test_private_key_us', 'abcd1234'),
    ('Germany Server', 'Frankfurt', 'DE', 'de.vpn.example.com', 443, 'test_public_key_de', 'test_private_key_de', 'efgh5678'),
    ('Singapore Server', 'Singapore', 'SG', 'sg.vpn.example.com', 443, 'test_public_key_sg', 'test_private_key_sg', 'ijkl9012'),
    ('Japan Server', 'Tokyo', 'JP', 'jp.vpn.example.com', 443, 'test_public_key_jp', 'test_private_key_jp', 'mnop3456');
