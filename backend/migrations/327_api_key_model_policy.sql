-- Key-specific administrator policy. Existing keys keep the request's model and effort.
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS model_mapping JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS reasoning_effort VARCHAR(16) NOT NULL DEFAULT '';
