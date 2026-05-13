-- Снимаем override триала по кампаниям. Существующие subscriptions не трогаем
-- (у них expires_at уже зафиксирован).
ALTER TABLE campaigns DROP COLUMN IF EXISTS trial_duration_days;
