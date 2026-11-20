-- Independent HTTP-pressure review; no scheduled quality plan ownership.
CREATE TABLE oauth_429_tasks (
 account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 model TEXT NOT NULL CHECK (length(model) BETWEEN 1 AND 256),
 state TEXT NOT NULL CHECK (state IN ('observing','pending','running','complete','cancelled')),
 data JSONB NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (account_id, model)
);
CREATE INDEX oauth_429_pending_idx ON oauth_429_tasks(updated_at) WHERE state IN ('pending','running');
CREATE TABLE oauth_429_daily_budget (
 day DATE PRIMARY KEY,
 reserved INTEGER NOT NULL CHECK (reserved >= 0)
);
-- Fence stop/resume and disable/re-enable even if an old configuration value is
-- restored before a late result returns. The worker cannot revive a cancelled task.
CREATE FUNCTION cancel_oauth_429_account_tasks() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.schedulable IS DISTINCT FROM OLD.schedulable OR
    NEW.deleted_at IS DISTINCT FROM OLD.deleted_at OR
    NEW.status IS DISTINCT FROM OLD.status OR
    NEW.credentials->'model_mapping' IS DISTINCT FROM OLD.credentials->'model_mapping' THEN
  UPDATE oauth_429_tasks SET state='cancelled',updated_at=clock_timestamp()
  WHERE account_id=NEW.id AND state IN ('pending','running');
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER oauth_429_account_fence AFTER UPDATE ON accounts
 FOR EACH ROW EXECUTE FUNCTION cancel_oauth_429_account_tasks();
CREATE FUNCTION cancel_oauth_429_config_tasks() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.key='smart_ops_oauth_auto_config' AND (TG_OP='DELETE' OR NEW.value IS DISTINCT FROM OLD.value) THEN
  UPDATE oauth_429_tasks SET state='cancelled',updated_at=clock_timestamp()
  WHERE state IN ('pending','running');
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER oauth_429_config_fence AFTER UPDATE OR DELETE ON settings
 FOR EACH ROW EXECUTE FUNCTION cancel_oauth_429_config_tasks();
