DROP INDEX IF EXISTS idx_vpn_servers_priority;
ALTER TABLE vpn_servers DROP COLUMN IF EXISTS priority;
