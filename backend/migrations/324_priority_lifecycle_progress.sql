-- Durable fair inventory and source/refresh coverage. Additive to 323; old
-- binaries keep writing canonical usage/timing rows through the same triggers.
CREATE TABLE IF NOT EXISTS priority_lifecycle_scopes (
    identity JSONB PRIMARY KEY,
    source_revision BIGINT NOT NULL DEFAULT 0,
    published_revision BIGINT NOT NULL DEFAULT 0,
    last_collected_at TIMESTAMPTZ,
    last_processed_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    refresh_state TEXT NOT NULL DEFAULT 'QUEUED'
);
ALTER TABLE priority_lifecycle_outbox ADD COLUMN IF NOT EXISTS scope_identity JSONB;
ALTER TABLE priority_lifecycle_outbox ADD COLUMN IF NOT EXISTS account_id BIGINT;
ALTER TABLE priority_lifecycle_statistics ADD COLUMN IF NOT EXISTS source_revision BIGINT NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS priority_lifecycle_coverage (
    category TEXT PRIMARY KEY,
    sequence BIGSERIAL NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    last_error TEXT NOT NULL
);

CREATE OR REPLACE FUNCTION priority_lifecycle_scope_identity(g BIGINT,m TEXT,e TEXT,k SMALLINT) RETURNS JSONB AS $$
    SELECT jsonb_build_object('group_id',g,'model',m,'endpoint',
      CASE rtrim(COALESCE(e,''),'/') WHEN '/responses' THEN '/v1/responses' WHEN '/v1/responses' THEN '/v1/responses'
      WHEN '/chat/completions' THEN '/v1/chat/completions' WHEN '/v1/chat/completions' THEN '/v1/chat/completions'
      WHEN '/messages' THEN '/v1/messages' WHEN '/v1/messages' THEN '/v1/messages' ELSE 'unknown' END,'request_type',k);
$$ LANGUAGE sql IMMUTABLE;

CREATE OR REPLACE FUNCTION priority_lifecycle_queue_scope(g BIGINT,m TEXT,e TEXT,k SMALLINT,u BIGINT,a BIGINT,kind_value TEXT) RETURNS void AS $$
DECLARE identity_value JSONB; event_sequence BIGINT;
BEGIN
    identity_value=priority_lifecycle_scope_identity(g,m,e,k);
    INSERT INTO priority_lifecycle_outbox(usage_log_id,kind,scope_identity,account_id)
    VALUES(u,kind_value || ':' || identity_value::text,identity_value,a)
    ON CONFLICT(usage_log_id,kind) DO UPDATE
      SET sequence=nextval('priority_lifecycle_outbox_sequence_seq'),committed_at=clock_timestamp(),processed_at=NULL,
          scope_identity=EXCLUDED.scope_identity,account_id=EXCLUDED.account_id
    RETURNING sequence INTO event_sequence;
    INSERT INTO priority_lifecycle_scopes(identity,source_revision)
    VALUES(identity_value,event_sequence) ON CONFLICT(identity) DO UPDATE
      SET source_revision=GREATEST(priority_lifecycle_scopes.source_revision+1,EXCLUDED.source_revision),refresh_state='QUEUED';
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION priority_lifecycle_usage_event() RETURNS trigger AS $$
BEGIN
    IF TG_OP='UPDATE' THEN
      PERFORM priority_lifecycle_queue_scope(OLD.group_id,COALESCE(NULLIF(OLD.requested_model,''),OLD.model),OLD.inbound_endpoint,OLD.request_type,OLD.id,OLD.account_id,'usage');
    END IF;
    PERFORM priority_lifecycle_queue_scope(NEW.group_id,COALESCE(NULLIF(NEW.requested_model,''),NEW.model),NEW.inbound_endpoint,NEW.request_type,NEW.id,NEW.account_id,'usage');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE OR REPLACE FUNCTION priority_lifecycle_timing_event() RETURNS trigger AS $$
DECLARE u RECORD;
BEGIN
    SELECT * INTO u FROM usage_logs WHERE id=NEW.usage_log_id;
    IF FOUND THEN
      PERFORM priority_lifecycle_queue_scope(u.group_id,COALESCE(NULLIF(u.requested_model,''),u.model),u.inbound_endpoint,u.request_type,u.id,u.account_id,'timing:' || NEW.trace_id::text);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Backward-compatible inventory seeding from old publishers and pending facts.
-- No new business evidence, no account/config/monetary mutation.
INSERT INTO priority_lifecycle_scopes(identity)
SELECT DISTINCT priority_lifecycle_scope_identity((snapshot->'scope'->>'group_id')::bigint,
 snapshot->'scope'->>'model',snapshot->'scope'->>'endpoint',(snapshot->'scope'->>'request_type')::smallint)
FROM priority_lifecycle_statistics ON CONFLICT DO NOTHING;
INSERT INTO priority_lifecycle_scopes(identity)
SELECT DISTINCT priority_lifecycle_scope_identity(u.group_id,COALESCE(NULLIF(u.requested_model,''),u.model),u.inbound_endpoint,u.request_type)
FROM usage_logs u JOIN priority_lifecycle_outbox o ON o.usage_log_id=u.id WHERE o.processed_at IS NULL
ON CONFLICT DO NOTHING;
