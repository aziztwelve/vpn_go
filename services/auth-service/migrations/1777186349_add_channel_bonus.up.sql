-- Add channel bonus tracking to users table
ALTER TABLE users 
ADD COLUMN IF NOT EXISTS channel_bonus_claimed BOOLEAN DEFAULT false,
ADD COLUMN IF NOT EXISTS channel_bonus_claimed_at TIMESTAMP WITH TIME ZONE;

COMMENT ON COLUMN users.channel_bonus_claimed IS 'Whether user claimed +3 days bonus for channel subscription';
COMMENT ON COLUMN users.channel_bonus_claimed_at IS 'When the channel bonus was claimed';
