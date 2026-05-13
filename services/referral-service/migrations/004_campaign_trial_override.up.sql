-- 004: Per-campaign trial override.
-- См. docs/tasks/19-campaign-trial-override.md
--
-- Колонка опциональна:
--   NULL                    → дефолт subscription_plans.id=99 (3 дня)
--   3 / 7 / 15 / 30 / 60 / 90 → override длительности триала для юзеров,
--                             пришедших по deep-link'у этой кампании
-- Резолв override'а делает subscription-service.StartTrialTx через JOIN
-- на user_attribution (которая уже зафиксирована к моменту StartTrial).
-- Юзерская реф-программа (referral_links) не затрагивается.

ALTER TABLE campaigns
    ADD COLUMN IF NOT EXISTS trial_duration_days INT
        CHECK (trial_duration_days IS NULL OR trial_duration_days IN (3,7,15,30,60,90));

COMMENT ON COLUMN campaigns.trial_duration_days IS
    'Override длительности триала (дни) для юзеров, пришедших по src_<slug>. NULL = дефолт subscription_plans.id=99 (3 дня).';
