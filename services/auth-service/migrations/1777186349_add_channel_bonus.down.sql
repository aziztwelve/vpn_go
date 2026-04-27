-- Remove channel bonus tracking
ALTER TABLE users 
DROP COLUMN IF EXISTS channel_bonus_claimed,
DROP COLUMN IF EXISTS channel_bonus_claimed_at;
