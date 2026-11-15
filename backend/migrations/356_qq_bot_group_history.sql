-- Shared history starts empty: never expose existing per-member conversations.
CREATE TABLE IF NOT EXISTS qq_bot_group_messages (
    id BIGSERIAL PRIMARY KEY,
    app_id TEXT NOT NULL,
    group_id TEXT NOT NULL,
    message_key TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content TEXT NOT NULL,
    context_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (app_id, group_id, message_key)
);
CREATE INDEX IF NOT EXISTS idx_qq_bot_group_messages_recent
    ON qq_bot_group_messages (app_id, group_id, context_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_qq_bot_group_messages_expiry
    ON qq_bot_group_messages (created_at);

-- Older references and command replies remain private to the explicit quote.
ALTER TABLE qq_bot_message_references
    ADD COLUMN IF NOT EXISTS shareable BOOLEAN NOT NULL DEFAULT FALSE;
