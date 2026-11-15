-- Existing shared replies may already combine several members' conversations.
-- Do not backfill ownership: only new, explicitly scoped history is used by
-- member-directed replies. Existing rows remain available for ambient context.
ALTER TABLE qq_bot_group_messages ADD COLUMN IF NOT EXISTS member_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_qq_bot_group_messages_member
 ON qq_bot_group_messages (app_id,group_id,member_id,context_at DESC,id DESC);
