ALTER TABLE lottery_pools ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE lottery_prizes ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

INSERT INTO settings (key, value, updated_at)
VALUES ('lottery_enabled', 'false', NOW())
ON CONFLICT (key) DO NOTHING;
