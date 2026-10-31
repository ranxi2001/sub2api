-- Original evidence JSON and absolute expiry remain immutable across policy
-- reevaluations. Durable transition events are separate from statistics TTL.
ALTER TABLE priority_lifecycle_policy ADD COLUMN IF NOT EXISTS evaluated_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE priority_lifecycle_policy ADD COLUMN IF NOT EXISTS evaluated_at TIMESTAMPTZ;
CREATE TABLE IF NOT EXISTS priority_lifecycle_evidence (
    scope_key TEXT NOT NULL,
    account_id BIGINT NOT NULL,
    evidence_id TEXT NOT NULL,
    evidence JSONB NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(scope_key,account_id,evidence_id)
);
INSERT INTO priority_lifecycle_evidence(scope_key,account_id,evidence_id,evidence)
SELECT scope_key,account_id,evidence_id,state FROM priority_lifecycle_policy
ON CONFLICT DO NOTHING;
