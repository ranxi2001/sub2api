-- Automatic rewards share the existing fulfillment workflow, without issuing
-- redeemable codes. The account-scoped source key prevents duplicate grants.
ALTER TABLE qq_bot_reward_codes ALTER COLUMN code_hash DROP NOT NULL;
ALTER TABLE qq_bot_reward_codes ADD COLUMN IF NOT EXISTS source_kind TEXT NOT NULL DEFAULT 'code';
ALTER TABLE qq_bot_reward_codes ADD COLUMN IF NOT EXISTS source_key TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS qq_bot_reward_codes_source_key ON qq_bot_reward_codes(source_key);
ALTER TABLE qq_bot_reward_codes DROP CONSTRAINT IF EXISTS qq_bot_reward_codes_source_check;
ALTER TABLE qq_bot_reward_codes ADD CONSTRAINT qq_bot_reward_codes_source_check CHECK (
    (source_kind='code' AND source_key IS NULL AND code_hash IS NOT NULL)
    OR (source_kind IN ('growth','streak') AND source_key IS NOT NULL AND code_hash IS NULL AND status IN ('pending','fulfilled'))
);
ALTER TABLE qq_bot_rewards DROP CONSTRAINT IF EXISTS qq_bot_rewards_kind_check;
ALTER TABLE qq_bot_rewards ADD CONSTRAINT qq_bot_rewards_kind_check CHECK (kind IN ('checkin','invite','lottery','redemption','growth','streak'));
