-- Persistent AI operations ownership. No account credentials or request bodies.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS ai_ops_revision BIGINT NOT NULL DEFAULT 1;

-- Usage/quota/ticket refreshes must not invalidate an operator's configuration
-- revision. All unrecognised extra keys remain configuration (fail closed).
CREATE OR REPLACE FUNCTION ai_ops_configuration_extra(input JSONB) RETURNS JSONB
LANGUAGE sql IMMUTABLE AS $$
 -- Legacy extra values may be arrays, scalars or JSON null. Preserve their
 -- identity for revision checks without passing them to jsonb_each.
 SELECT CASE WHEN jsonb_typeof(input) = 'object' THEN (
 SELECT COALESCE(jsonb_object_agg(key,value),'{}'::jsonb)
 FROM jsonb_each(CASE WHEN jsonb_typeof(input) = 'object' THEN input ELSE '{}'::jsonb END)
 WHERE key NOT IN ('codex_usage_updated_at','codex_credits_snapshot','codex_referral_snapshot',
   'grok_billing_snapshot','session_window_utilization')
 AND key !~ '^(codex_primary_|codex_secondary_|codex_5h_|codex_7d_|codex_reset_credit_|codex_turn_ticket:|passive_usage_|upstream_billing_probe|upstream_billing_rate_sync|ollama_cloud_usage)'
 ) ELSE COALESCE(input,'{}'::jsonb) END
$$;

CREATE OR REPLACE FUNCTION ai_ops_account_revision() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.platform,NEW.type,NEW.credentials,NEW.proxy_id,NEW.concurrency,NEW.load_factor,
   NEW.priority,NEW.status,NEW.schedulable,NEW.expires_at,NEW.auto_pause_on_expired,
   NEW.deleted_at,NEW.parent_account_id,NEW.quota_dimension)
 IS DISTINCT FROM ROW(OLD.platform,OLD.type,OLD.credentials,OLD.proxy_id,OLD.concurrency,OLD.load_factor,
   OLD.priority,OLD.status,OLD.schedulable,OLD.expires_at,OLD.auto_pause_on_expired,
   OLD.deleted_at,OLD.parent_account_id,OLD.quota_dimension)
 OR ai_ops_configuration_extra(NEW.extra) IS DISTINCT FROM ai_ops_configuration_extra(OLD.extra) THEN
   NEW.ai_ops_revision := OLD.ai_ops_revision + 1;
 ELSE
   NEW.ai_ops_revision := GREATEST(OLD.ai_ops_revision,NEW.ai_ops_revision);
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS ai_ops_account_revision_trigger ON accounts;
CREATE TRIGGER ai_ops_account_revision_trigger BEFORE UPDATE ON accounts
 FOR EACH ROW EXECUTE FUNCTION ai_ops_account_revision();

CREATE OR REPLACE FUNCTION ai_ops_membership_revision() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW IS NOT DISTINCT FROM OLD THEN RETURN NULL; END IF;
 IF TG_OP IN ('UPDATE','DELETE') THEN
   UPDATE accounts SET ai_ops_revision=ai_ops_revision+1 WHERE id=OLD.account_id;
 END IF;
 IF TG_OP='INSERT' OR (TG_OP='UPDATE' AND NEW.account_id<>OLD.account_id) THEN
   UPDATE accounts SET ai_ops_revision=ai_ops_revision+1 WHERE id=NEW.account_id;
 END IF;
 RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS ai_ops_membership_revision_trigger ON account_groups;
CREATE TRIGGER ai_ops_membership_revision_trigger AFTER INSERT OR UPDATE OR DELETE ON account_groups
 FOR EACH ROW EXECUTE FUNCTION ai_ops_membership_revision();

CREATE TABLE IF NOT EXISTS ai_ops_policy (
 id SMALLINT PRIMARY KEY CHECK(id=1), revision BIGINT NOT NULL DEFAULT 0,
 config JSONB NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), actor_id BIGINT
);
INSERT INTO ai_ops_policy(id,config) VALUES(1,'{"mode":"disabled","halted":true,"group_id":0,"accounts":[],"auto_actions":[]}')
 ON CONFLICT(id) DO NOTHING;

CREATE TABLE IF NOT EXISTS ai_ops_proposals (
 id UUID PRIMARY KEY, run_id TEXT NOT NULL, proposal_index INTEGER NOT NULL,
 account_id BIGINT NOT NULL, digest TEXT NOT NULL, policy_revision BIGINT NOT NULL,
 target_revision BIGINT NOT NULL, status TEXT NOT NULL CHECK(status IN ('ready','blocked','applied','expired','conflict')),
 data JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL,
 actor_id BIGINT NOT NULL,
 UNIQUE(run_id,proposal_index)
);
CREATE INDEX IF NOT EXISTS ai_ops_proposals_due_idx ON ai_ops_proposals(expires_at) WHERE status='ready';

-- Audit records deliberately survive account/user deletion. A proposal can
-- cause at most one action, including retries after a lost HTTP response.
CREATE TABLE IF NOT EXISTS ai_ops_actions (
 id UUID PRIMARY KEY, proposal_id UUID NOT NULL UNIQUE REFERENCES ai_ops_proposals(id),
 account_id BIGINT NOT NULL, status TEXT NOT NULL CHECK(status IN
 ('propagation_pending','observing','succeeded','inconclusive','rolled_back','conflict','failed')),
 data JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
 actor_id BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ai_ops_actions_account_idx ON ai_ops_actions(account_id,created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS ai_ops_actions_active_account_idx ON ai_ops_actions(account_id)
 WHERE status IN ('propagation_pending','observing');
CREATE INDEX IF NOT EXISTS ai_ops_actions_budget_idx ON ai_ops_actions(created_at);

CREATE TABLE IF NOT EXISTS ai_ops_controls (
 account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 ai_paused BOOLEAN NOT NULL DEFAULT false,
 revision BIGINT NOT NULL DEFAULT 0,
 owner_action_id UUID REFERENCES ai_ops_actions(id),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS ai_ops_controls_paused_idx ON ai_ops_controls(account_id) WHERE ai_paused;

CREATE TABLE IF NOT EXISTS ai_ops_probes (
 id UUID PRIMARY KEY, account_id BIGINT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('reserved','passed','failed','unknown')),
 data JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL,
 actor_id BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ai_ops_probes_account_idx ON ai_ops_probes(account_id,created_at DESC);
CREATE INDEX IF NOT EXISTS ai_ops_probes_due_idx ON ai_ops_probes(expires_at) WHERE status='reserved';
