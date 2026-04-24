-- subscription_token — публичный секрет для импорта подписки клиентом
-- (Happ, V2RayNG, Hiddify, Streisand, …). Юзер открывает на устройстве
-- URL `/api/v1/subscription/<token>` → получает base64/json конфиг.
--
-- Зачем отдельно от `uuid`:
--   - uuid — идентификатор внутри Xray (VLESS user id). Если он утечёт —
--     и так виден в VLESS-ссылке внутри подписки, ротация дорогая.
--   - subscription_token — публичный API-ключ уровня "ссылка на мою подписку".
--     Можно ротировать независимо (юзер нажал "сменить ссылку подписки").
--
-- Формат: 48 hex-символов (24 байта случайности). Генерируется на Go
-- при создании vpn_user, а для существующих строк — md5(uuid+id+clock).
-- 48 hex достаточно (~192 бита энтропии; на коллизию можно не смотреть).

ALTER TABLE vpn_users
    ADD COLUMN IF NOT EXISTS subscription_token VARCHAR(64);

-- Заполняем существующие строки (если накатываем на живую БД).
UPDATE vpn_users
SET subscription_token = substring(
    md5(id::text || uuid || clock_timestamp()::text) || md5(random()::text),
    1, 48
)
WHERE subscription_token IS NULL;

ALTER TABLE vpn_users
    ALTER COLUMN subscription_token SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_vpn_users_subscription_token
    ON vpn_users(subscription_token);
