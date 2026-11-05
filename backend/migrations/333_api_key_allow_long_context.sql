-- Preserve current behavior until an administrator disables long context per key.
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS allow_long_context BOOLEAN NOT NULL DEFAULT TRUE;
