-- Rollback: drop inbound_tag + unique index. Seed не восстанавливаем — это dev-данные.
DROP INDEX IF EXISTS idx_vpn_servers_name;
ALTER TABLE vpn_servers DROP COLUMN IF EXISTS inbound_tag;
