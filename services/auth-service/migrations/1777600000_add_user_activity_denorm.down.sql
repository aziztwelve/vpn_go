DROP INDEX IF EXISTS idx_users_last_traffic_at;
DROP INDEX IF EXISTS idx_users_first_connection_null;

ALTER TABLE users
    DROP COLUMN IF EXISTS last_traffic_at,
    DROP COLUMN IF EXISTS first_connection_at;
