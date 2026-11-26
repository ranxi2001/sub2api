-- Claim order includes abandoned runs. Only the latest per-model streak is retained.
ALTER TABLE scheduled_test_plans
    ADD COLUMN IF NOT EXISTS quality_round_seq bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS quality_429_state jsonb;
