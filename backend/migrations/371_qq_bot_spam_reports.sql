-- Only enabled groups retain a bounded, short-lived encrypted text snapshot.
-- Original QQ message IDs, not quote text or reporter-supplied authors, bind evidence.
CREATE TABLE IF NOT EXISTS qq_bot_spam_report_sources (
 app_id TEXT NOT NULL, sandbox BOOLEAN NOT NULL, group_id TEXT NOT NULL,
 message_id TEXT NOT NULL, ref_idx TEXT NOT NULL DEFAULT '', member_id TEXT NOT NULL,
 member_role TEXT NOT NULL DEFAULT '', is_bot BOOLEAN NOT NULL DEFAULT FALSE,
 eligible BOOLEAN NOT NULL DEFAULT FALSE, content_encrypted TEXT NOT NULL DEFAULT '', content_hash TEXT NOT NULL DEFAULT '',
 policy_hash TEXT NOT NULL, sent_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(app_id,sandbox,group_id,message_id)
);
CREATE INDEX IF NOT EXISTS qq_bot_spam_report_sources_reference ON qq_bot_spam_report_sources(app_id,sandbox,group_id,ref_idx) WHERE ref_idx<>'';
CREATE INDEX IF NOT EXISTS qq_bot_spam_report_sources_expiry ON qq_bot_spam_report_sources(sent_at);
CREATE INDEX IF NOT EXISTS qq_bot_spam_report_sources_member ON qq_bot_spam_report_sources(app_id,sandbox,group_id,member_id,sent_at DESC);

-- Deduplicate the target message across reporters, workers, and restarts.
-- No raw source text, URL, model response, or evidence is retained in audit rows.
CREATE TABLE IF NOT EXISTS qq_bot_spam_reports (
 id TEXT PRIMARY KEY, app_id TEXT NOT NULL, sandbox BOOLEAN NOT NULL, group_id TEXT NOT NULL,
 reporter_id TEXT NOT NULL, message_id TEXT NOT NULL, member_id TEXT NOT NULL,
 policy_hash TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'evaluating',
 category TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', incident_id TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), finished_at TIMESTAMPTZ,
 UNIQUE(app_id,sandbox,group_id,message_id)
);
CREATE INDEX IF NOT EXISTS qq_bot_spam_reports_reporter ON qq_bot_spam_reports(app_id,sandbox,reporter_id,created_at DESC);
CREATE INDEX IF NOT EXISTS qq_bot_spam_reports_group ON qq_bot_spam_reports(app_id,sandbox,group_id,created_at DESC);
CREATE INDEX IF NOT EXISTS qq_bot_spam_reports_incident ON qq_bot_spam_reports(incident_id) WHERE incident_id<>'';
CREATE INDEX IF NOT EXISTS qq_bot_spam_reports_expiry ON qq_bot_spam_reports(created_at);
