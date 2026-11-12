-- Zero preserves the existing allow_long_context and model-threshold policy.
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS context_token_limit INTEGER NOT NULL DEFAULT 0
    CHECK (context_token_limit >= 0);

COMMENT ON COLUMN api_keys.context_token_limit IS
    'Administrator-managed input token ceiling; zero inherits the long context policy';
