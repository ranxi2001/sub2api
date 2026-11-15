-- Only retain messages actually delivered by this bot, scoped to their group.
-- No callback auth_token, attachment URL, image bytes or private history here.
CREATE TABLE IF NOT EXISTS qq_bot_message_references (
    app_id TEXT NOT NULL,
    group_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    ref_idx TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    is_image BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (app_id, group_id, message_id)
);
CREATE INDEX IF NOT EXISTS qq_bot_message_references_ref
    ON qq_bot_message_references(app_id, group_id, ref_idx) WHERE ref_idx <> '';
CREATE INDEX IF NOT EXISTS qq_bot_message_references_expiry
    ON qq_bot_message_references(created_at);
