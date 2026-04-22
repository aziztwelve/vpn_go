-- Добавляем inbound_tag: имя inbound'а внутри Xray, куда вызывать AlterInbound(ADD/REMOVE user).
ALTER TABLE vpn_servers ADD COLUMN IF NOT EXISTS inbound_tag VARCHAR(64) NOT NULL DEFAULT 'vless-reality-in';

-- Чистим mock-сервера из миграции 001 — реальный локальный сервер VPN Service
-- засеидит сам при старте (app.seedLocalServer), беря host/keys/short_id из env.
DELETE FROM vpn_servers;

-- UNIQUE(name) нужен для UpsertServerByName (ON CONFLICT name).
-- Использую уникальный индекс вместо constraint — идемпотентно через IF NOT EXISTS.
CREATE UNIQUE INDEX IF NOT EXISTS idx_vpn_servers_name ON vpn_servers(name);
