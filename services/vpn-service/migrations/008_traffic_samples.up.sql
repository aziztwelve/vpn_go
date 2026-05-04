-- traffic_samples — точечные сэмплы дельт трафика per (vpn_user, server).
--
-- Пишет TrafficCron в vpn-service: каждые 5 минут дёргает
--   xray.QueryStats(pattern="user>>>", reset=TRUE) на каждом активном сервере,
-- парсит результаты вида "user{vpn_user_id}@vpn.local>>>traffic>>>{uplink|downlink}",
-- и делает batch-INSERT в эту таблицу только для ненулевых дельт (чтобы
-- не пухнуть при 95% неактивных юзеров).
--
-- reset=true гарантирует что мы не двоим счёт: после каждого QueryStats
-- счётчик в xray обнуляется, и в следующий тик мы видим только новый
-- инкремент. При рестарте xray in-memory счётчик теряется — ок, потеряем
-- максимум 5 минут трафика (≈50 MB на активного юзера, приемлемо).
--
-- Семантика vs старого active_connections:
--   active_connections.last_seen  — «клиент скачал subscription URL ИЛИ
--                                   юзер пошевелился через VPN» — неоднозначно.
--   traffic_samples              — «точно эти байты прошли через VPN»;
--                                   отсутствие записей = не пользуется.
--
-- Источники запросов (retention / caps):
--   * «был ли хоть один байт за [T1, T2]?» — EXISTS(SELECT 1 FROM traffic_samples)
--   * «сколько GB за период?» — SUM(uplink_bytes+downlink_bytes)
--   * «когда был последний трафик?» — MAX(collected_at) ИЛИ денормализованное
--                                     users.last_traffic_at (см. миграцию auth-service)

CREATE TABLE IF NOT EXISTS traffic_samples (
    id BIGSERIAL PRIMARY KEY,
    vpn_user_id    BIGINT NOT NULL REFERENCES vpn_users(id) ON DELETE CASCADE,
    server_id      INTEGER NOT NULL REFERENCES vpn_servers(id) ON DELETE CASCADE,
    uplink_bytes   BIGINT NOT NULL DEFAULT 0 CHECK (uplink_bytes   >= 0),
    downlink_bytes BIGINT NOT NULL DEFAULT 0 CHECK (downlink_bytes >= 0),
    collected_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Основной паттерн запросов: «трафик юзера X с момента T»
CREATE INDEX IF NOT EXISTS idx_traffic_samples_user_time
    ON traffic_samples (vpn_user_id, collected_at DESC);

-- Для retention-сегментации массово: «кто шевелился между T1 и T2»
CREATE INDEX IF NOT EXISTS idx_traffic_samples_collected_at
    ON traffic_samples (collected_at DESC);

-- Для cleanup-крона (DELETE старше 90 дней)
-- Этот индекс частично покрывается предыдущим, отдельно не создаём.

COMMENT ON TABLE traffic_samples IS
    'Delta samples from xray stats API, written every 5 min by TrafficCron. '
    'Zero-deltas are NOT inserted. Raw bytes only, aggregation is done at query time. '
    'Cleanup retains last 90 days.';
