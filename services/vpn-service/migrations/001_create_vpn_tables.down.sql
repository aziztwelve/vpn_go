-- Drop in reverse order of creation to respect FK constraints.
DROP INDEX IF EXISTS idx_active_connections_last_seen;
DROP INDEX IF EXISTS idx_active_connections_vpn_user_id;
DROP INDEX IF EXISTS idx_vpn_users_uuid;
DROP INDEX IF EXISTS idx_vpn_users_user_id;

DROP TABLE IF EXISTS active_connections;
DROP TABLE IF EXISTS vpn_users;
DROP TABLE IF EXISTS vpn_servers;
