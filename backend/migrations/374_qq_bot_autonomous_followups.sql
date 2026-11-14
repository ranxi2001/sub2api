-- Persist bounded continuation counts across workers and restarts.
ALTER TABLE qq_bot_autonomous_state
    ADD COLUMN IF NOT EXISTS followup_count INTEGER NOT NULL DEFAULT 0;
