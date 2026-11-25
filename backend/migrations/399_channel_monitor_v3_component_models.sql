ALTER TABLE channel_monitor_v3_components
    ADD COLUMN IF NOT EXISTS models TEXT[] NOT NULL DEFAULT '{}';
