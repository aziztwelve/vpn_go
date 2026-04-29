-- Возврат NOT NULL. Перед накатом убедись что в active_connections
-- нет строк с server_id IS NULL (или они тебе не нужны):
--   DELETE FROM active_connections WHERE server_id IS NULL;
ALTER TABLE active_connections ALTER COLUMN server_id SET NOT NULL;
