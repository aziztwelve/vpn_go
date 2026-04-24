DROP INDEX IF EXISTS idx_vpn_users_subscription_token;
ALTER TABLE vpn_users DROP COLUMN IF EXISTS subscription_token;
