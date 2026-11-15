-- Receipts contain static decisions and hashes, never prompts or credentials.
ALTER TABLE qq_bot_jobs ADD COLUMN IF NOT EXISTS image_decision JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE qq_bot_message_references ADD COLUMN IF NOT EXISTS source_job_id TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS qq_bot_pending_image_plans (
 app_id TEXT NOT NULL, group_id TEXT NOT NULL, member_id TEXT NOT NULL,
 job_id TEXT NOT NULL REFERENCES qq_bot_jobs(id) ON DELETE CASCADE,
 source_message_id TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('generate_image','edit_image')),
 prompt TEXT NOT NULL CHECK(char_length(prompt) BETWEEN 1 AND 6000),
 request_quote TEXT NOT NULL CHECK(char_length(request_quote) BETWEEN 1 AND 300),
 expires_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(app_id,group_id,member_id)
);
CREATE INDEX IF NOT EXISTS qq_bot_pending_image_plans_expiry ON qq_bot_pending_image_plans(expires_at);
