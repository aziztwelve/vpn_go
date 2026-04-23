-- Откат: возвращаем UNIQUE(name) и убираем UNIQUE(host, port).
-- Если в БД уже есть дубликаты имён — миграция упадёт; это ожидаемо,
-- тогда сначала придётся их вычистить вручную.
DROP INDEX IF EXISTS idx_vpn_servers_host_port;
DROP INDEX IF EXISTS idx_vpn_servers_name;
CREATE UNIQUE INDEX IF NOT EXISTS idx_vpn_servers_name ON vpn_servers(name);
