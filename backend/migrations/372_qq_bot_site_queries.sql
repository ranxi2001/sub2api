-- Keep query outcome separate from model execution and QQ delivery. No raw
-- provider errors, account IDs, result bodies or credentials are stored here.
ALTER TABLE qq_bot_jobs ADD COLUMN IF NOT EXISTS site_query JSONB NOT NULL DEFAULT '{}'::jsonb;
CREATE INDEX IF NOT EXISTS idx_qq_bot_jobs_site_query_member
 ON qq_bot_jobs(app_id,group_id,member_id,created_at DESC,id DESC)
 WHERE site_query->>'kind' IS NOT NULL;
