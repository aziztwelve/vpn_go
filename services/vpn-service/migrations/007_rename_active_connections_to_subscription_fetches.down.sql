-- Откат: возвращаем таблицу subscription_fetches → active_connections.
-- server_id колонку в up.sql мы не трогали, значит и в down-е ничего
-- восстанавливать не нужно.

ALTER TABLE subscription_fetches RENAME TO active_connections;

ALTER INDEX subscription_fetches_pkey
    RENAME TO active_connections_pkey;
ALTER INDEX subscription_fetches_vpn_user_id_device_identifier_key
    RENAME TO active_connections_vpn_user_id_device_identifier_key;
ALTER INDEX idx_subscription_fetches_last_seen
    RENAME TO idx_active_connections_last_seen;
ALTER INDEX idx_subscription_fetches_vpn_user_id
    RENAME TO idx_active_connections_vpn_user_id;

ALTER TABLE active_connections
    RENAME CONSTRAINT subscription_fetches_server_id_fkey
    TO active_connections_server_id_fkey;
ALTER TABLE active_connections
    RENAME CONSTRAINT subscription_fetches_vpn_user_id_fkey
    TO active_connections_vpn_user_id_fkey;
