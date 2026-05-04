-- Переименование active_connections → subscription_fetches.
--
-- Причина: таблица одновременно ловила два разных события:
--   1) HTTP-запрос клиента на /subscription/{token} (Happ, V2rayTUN,
--      Telegram link-preview) — UpsertDeviceTouch пишет строку c
--      server_id=NULL, device_identifier=User-Agent.
--   2) Xray-heartbeat при росте трафика — UPDATE last_seen=NOW() для
--      ВСЕХ строк юзера.
-- По одному полю last_seen отличить «клиент обновил подписку» от
-- «юзер реально гонит трафик» нельзя. Это приводит к неправильным
-- retention-метрикам: 2026-05-03 по active_connections казалось что
-- 6/18 trial-юзеров подключались; по реальному xray-трафику — 1/18.
--
-- После этой миграции таблица обслуживает ТОЛЬКО subscription-fetch'и.
-- Heartbeat больше её не трогает (см. upcoming изменение в
-- services/vpn-service/internal/service/heartbeat.go). Реальный трафик
-- хранится в traffic_samples (миграция 008).
--
-- Важно: server_id колонку сохраняем (NULLable). Исторически legacy-путь
-- UpsertActiveConnection (per-server GetVLESSLink) пишет туда server_id —
-- и LoadCron читает для пересчёта vpn_servers.load_percent. Не трогаем,
-- чтобы не ломать legacy. Основной device-limit работает через
-- UNIQUE (vpn_user_id, device_identifier) и сервер-агностичен.

ALTER TABLE active_connections RENAME TO subscription_fetches;

-- Переименовываем индексы и ограничения на новое имя, чтобы не ловить
-- путаницу при чтении \d.
ALTER INDEX active_connections_pkey
    RENAME TO subscription_fetches_pkey;
ALTER INDEX active_connections_vpn_user_id_device_identifier_key
    RENAME TO subscription_fetches_vpn_user_id_device_identifier_key;
ALTER INDEX idx_active_connections_last_seen
    RENAME TO idx_subscription_fetches_last_seen;
ALTER INDEX idx_active_connections_vpn_user_id
    RENAME TO idx_subscription_fetches_vpn_user_id;

-- FK-констрейнты
ALTER TABLE subscription_fetches
    RENAME CONSTRAINT active_connections_server_id_fkey
    TO subscription_fetches_server_id_fkey;
ALTER TABLE subscription_fetches
    RENAME CONSTRAINT active_connections_vpn_user_id_fkey
    TO subscription_fetches_vpn_user_id_fkey;
