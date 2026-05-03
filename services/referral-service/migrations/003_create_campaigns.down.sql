-- Rollback в обратном порядке: сначала ALTER bot_starts, потом таблицы
-- (campaign_payouts/user_attribution/pending_campaigns зависят от campaigns).

DROP INDEX IF EXISTS idx_bot_starts_campaign;
ALTER TABLE bot_starts DROP COLUMN IF EXISTS campaign_id;

DROP TABLE IF EXISTS campaign_payouts;
DROP TABLE IF EXISTS user_attribution;
DROP TABLE IF EXISTS pending_campaigns;
DROP TABLE IF EXISTS campaigns;
