-- Меняем seed-identity локального Xray-сервера с name на (host, port).
--
-- Было: `UpsertServerByName` использовал UNIQUE(name) для ON CONFLICT.
-- Проблема: стоит админу переименовать сервер в UI/SQL (например,
-- `Local Xray (dev)` → `Finland`) — сидер при следующем рестарте
-- vpn-service'а не находит строку по имени и INSERT-ит дубликат
-- с тем же host/port. Получаем два одинаковых сервера.
--
-- После: seed идентифицирует сервер по физическому identity (host+port).
-- Имя/локацию/country_code админ меняет свободно, сидер их не трогает
-- (UPDATE теперь затрагивает только crypto-поля и api-эндпоинты Xray).

-- 1. Сносим UNIQUE-индекс по name. Обычный btree оставляем для быстрого
--    lookup'а по имени в админке (если когда-нибудь появится).
DROP INDEX IF EXISTS idx_vpn_servers_name;
CREATE INDEX IF NOT EXISTS idx_vpn_servers_name ON vpn_servers(name);

-- 2. UNIQUE(host, port) — новый ключ для ON CONFLICT в UpsertServerByHostPort.
--    На проде пара (host, port) определяет один физический Xray-инбаунд;
--    регистрировать два "сервера" с одинаковой парой бессмысленно.
CREATE UNIQUE INDEX IF NOT EXISTS idx_vpn_servers_host_port
    ON vpn_servers(host, port);
