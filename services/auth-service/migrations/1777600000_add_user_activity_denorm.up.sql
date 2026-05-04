-- Денормализованные поля в users для быстрой retention-сегментации.
--
-- Без них retention-фильтры ("юзеры без трафика за 24 часа", "юзеры с
-- первым коннектом > 1h от регистрации") требуют SUM / MAX по
-- traffic_samples на каждый тик RetentionCron — тяжело при росте базы.
-- Денормализуем и обновляем транзакционно из TrafficCron (он же пишет
-- traffic_samples — в той же транзакции UPDATE users).
--
-- Семантика:
--
-- first_connection_at
--   Момент самого раннего ненулевого инкремента трафика для юзера.
--   NULL = юзер ни разу не пустил ни байта через VPN. Ставится РАЗ
--   (COALESCE, см. TrafficCron — update игнорирует если уже не NULL).
--   Используется в retention-фильтре "trial_never_connected".
--
-- last_traffic_at
--   Момент самого последнего ненулевого инкремента. Обновляется на
--   каждый тик TrafficCron при наличии дельты у юзера. Используется
--   для "paid_churn_risk" (нет трафика >3d) и "trial_ending_idle".
--
-- Отличие от last_active_at:
--   last_active_at  — любая активность в боте / MiniApp (жал кнопки)
--   last_traffic_at — прошёл байт через xray
--   Это ДВЕ разные вещи: юзер может сидеть в боте не подключаясь,
--   либо пользоваться VPN но не открывать бот.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS first_connection_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_traffic_at     TIMESTAMPTZ;

-- Для сегмент-фильтров "WHERE first_connection_at IS NULL", "WHERE
-- last_traffic_at < NOW() - INTERVAL '3 days'". Partial-index на
-- NULL first_connection_at покрывает горячий trial_never_connected
-- кейс без затрат на активных юзеров.
CREATE INDEX IF NOT EXISTS idx_users_first_connection_null
    ON users(created_at) WHERE first_connection_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_users_last_traffic_at
    ON users(last_traffic_at)
    WHERE last_traffic_at IS NOT NULL;

COMMENT ON COLUMN users.first_connection_at IS
    'Момент первого ненулевого трафика (из traffic_samples). NULL = не подключался.';
COMMENT ON COLUMN users.last_traffic_at IS
    'Момент последнего ненулевого трафика. Обновляется TrafficCron.';

-- Backfill: для юзеров, у которых уже была активность (last_active_at),
-- ставим last_traffic_at = last_active_at как грубый baseline. На
-- следующем тике TrafficCron перезапишет реальным значением при
-- появлении нового трафика. Это корректно потому что:
--   - фильтры смотрят «last_traffic_at < NOW() - T»;
--   - завышение baseline → юзер «недавний», не попадает в retention —
--     безопасная сторона ошибки (не спамим напрасно).
-- first_connection_at намеренно оставляем NULL — мы не знаем был ли
-- реальный трафик в прошлом. TrafficCron поставит на первом реальном
-- инкременте. До этого все существующие юзеры попадут в
-- trial_never_connected если status=trial — и это ПРАВИЛЬНО
-- (мы не знаем подключался ли он, значит пришлём онбординг-гайд).
UPDATE users SET last_traffic_at = last_active_at
WHERE last_active_at IS NOT NULL AND last_traffic_at IS NULL;
