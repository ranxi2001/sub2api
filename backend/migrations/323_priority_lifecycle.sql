-- Additive shadow lifecycle only. Never changes billing, account flags or old
-- scheduler policy. Old binaries can continue inserting usage and timing rows.
CREATE TABLE IF NOT EXISTS priority_lifecycle_outbox (
    sequence BIGSERIAL PRIMARY KEY,
    usage_log_id BIGINT NOT NULL,
    kind TEXT NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    processed_at TIMESTAMPTZ,
    UNIQUE (usage_log_id, kind)
);
CREATE INDEX IF NOT EXISTS priority_lifecycle_outbox_pending
    ON priority_lifecycle_outbox(sequence) WHERE processed_at IS NULL;

CREATE OR REPLACE FUNCTION priority_lifecycle_usage_event() RETURNS trigger AS $$
BEGIN
    INSERT INTO priority_lifecycle_outbox(usage_log_id,kind)
    VALUES(NEW.id,'usage') ON CONFLICT(usage_log_id,kind) DO UPDATE
    SET sequence=nextval('priority_lifecycle_outbox_sequence_seq'), committed_at=clock_timestamp(),processed_at=NULL;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS priority_lifecycle_usage_committed ON usage_logs;
CREATE TRIGGER priority_lifecycle_usage_committed AFTER INSERT OR UPDATE OF first_token_ms,stream,requested_model,model,group_id,inbound_endpoint,request_type ON usage_logs
    FOR EACH ROW EXECUTE FUNCTION priority_lifecycle_usage_event();

-- Timing details can commit after usage. Requeue the same idempotent event;
-- sequence/watermark allocation is independent of usage ids and event time.
CREATE OR REPLACE FUNCTION priority_lifecycle_timing_event() RETURNS trigger AS $$
BEGIN
    INSERT INTO priority_lifecycle_outbox(usage_log_id,kind)
    VALUES(NEW.usage_log_id,'timing:' || NEW.trace_id::text)
    ON CONFLICT (usage_log_id,kind) DO UPDATE
    SET sequence=nextval('priority_lifecycle_outbox_sequence_seq'),
        committed_at=clock_timestamp(),processed_at=NULL;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS priority_lifecycle_timing_committed ON request_timing_details;
CREATE TRIGGER priority_lifecycle_timing_committed AFTER INSERT OR UPDATE ON request_timing_details
    FOR EACH ROW EXECUTE FUNCTION priority_lifecycle_timing_event();

CREATE TABLE IF NOT EXISTS priority_lifecycle_statistics (
    scope_key TEXT PRIMARY KEY,
    generation BIGINT NOT NULL,
    watermark BIGINT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    snapshot JSONB NOT NULL
);
CREATE TABLE IF NOT EXISTS priority_lifecycle_policy (
    scope_key TEXT NOT NULL,
    account_id BIGINT NOT NULL,
    evidence_id TEXT NOT NULL,
    evidence_at TIMESTAMPTZ NOT NULL,
    policy_revision TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    state JSONB NOT NULL,
    PRIMARY KEY(scope_key,account_id)
);
CREATE TABLE IF NOT EXISTS priority_lifecycle_policy_events (
    sequence BIGSERIAL PRIMARY KEY,
    scope_key TEXT NOT NULL,
    account_id BIGINT NOT NULL,
    evidence_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    event_at TIMESTAMPTZ NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    event JSONB NOT NULL,
    UNIQUE(scope_key,account_id,evidence_id,kind)
);
CREATE TABLE IF NOT EXISTS priority_lifecycle_decisions (
    sequence BIGSERIAL PRIMARY KEY,
    decision_id UUID NOT NULL UNIQUE,
    scope_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    record JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS priority_lifecycle_decisions_scope
    ON priority_lifecycle_decisions(scope_key,sequence DESC);
CREATE INDEX IF NOT EXISTS priority_lifecycle_events_scope
    ON priority_lifecycle_policy_events(scope_key,sequence DESC);
-- Retention/SLO are deliberately not invented here. No destructive cleanup or
-- historical backfill; a bounded compensation scan is performed by the worker.
