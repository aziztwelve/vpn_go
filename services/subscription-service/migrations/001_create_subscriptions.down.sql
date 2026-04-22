-- Drop in reverse order of creation to respect FK constraints.
DROP INDEX IF EXISTS idx_subscriptions_expires_at;
DROP INDEX IF EXISTS idx_subscriptions_status;
DROP INDEX IF EXISTS idx_subscriptions_user_id;

DROP TABLE IF EXISTS subscriptions;
DROP TABLE IF EXISTS device_addon_pricing;
DROP TABLE IF EXISTS subscription_plans;
