-- Bounded explicit-chat retry diagnostics only. Missing usage is unknown, not zero.
-- No raw provider errors, prompt content or credentials are stored.
ALTER TABLE qq_bot_jobs ADD COLUMN IF NOT EXISTS chat_execution JSONB;
