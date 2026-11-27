-- Keep metered client rejections out of the SLA success count.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS sla_excluded BOOLEAN NOT NULL DEFAULT FALSE;
