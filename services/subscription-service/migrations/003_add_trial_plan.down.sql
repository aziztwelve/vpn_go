DELETE FROM subscription_plans WHERE id = 99 AND is_trial = true;
ALTER TABLE subscription_plans DROP COLUMN IF EXISTS is_trial;
