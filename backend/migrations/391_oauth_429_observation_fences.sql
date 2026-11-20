-- Account epochs survive stop/resume, soft delete/restore and route changes.
-- The boundary also rejects observations still waiting in a gateway's queue.
CREATE TABLE oauth_429_account_epochs (
 account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 epoch BIGINT NOT NULL DEFAULT 1 CHECK (epoch > 0),
 valid_since TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO oauth_429_account_epochs(account_id) SELECT id FROM accounts;

-- Request identity retention is independent of the latest 200 statistics.
-- Each of the bounded task rows admits at most 8192 live request identities;
-- exhaustion rejects new observations instead of forgetting a live identity.
CREATE TABLE oauth_429_request_dedup (
 account_id BIGINT NOT NULL,
 model TEXT NOT NULL,
 request_hash TEXT NOT NULL CHECK (length(request_hash)=64),
 observed_at TIMESTAMPTZ NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(account_id,model,request_hash),
 FOREIGN KEY(account_id,model) REFERENCES oauth_429_tasks(account_id,model) ON DELETE CASCADE
);
CREATE INDEX oauth_429_dedup_expiry ON oauth_429_request_dedup(account_id,model,expires_at);
-- Preserve already accepted identities on upgrade; historical ring omissions
-- cannot be reconstructed, so existing reviews are cancelled below.
INSERT INTO oauth_429_request_dedup(account_id,model,request_hash,observed_at,expires_at)
 SELECT t.account_id,t.model,s->>'request',(s->>'at')::timestamptz,clock_timestamp()+interval '1 hour'
 FROM oauth_429_tasks t CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(t.data->'samples')='array' THEN t.data->'samples' ELSE '[]'::jsonb END) s
 WHERE length(s->>'request')=64 AND (s->>'at')::timestamptz>=clock_timestamp()-interval '1 hour'
 ON CONFLICT DO NOTHING;
UPDATE oauth_429_tasks SET state=CASE WHEN state IN ('pending','running') THEN 'cancelled' ELSE state END,
 data=jsonb_set(data,'{samples}','[]'),updated_at=clock_timestamp();

CREATE OR REPLACE FUNCTION cancel_oauth_429_account_tasks() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  INSERT INTO oauth_429_account_epochs(account_id) VALUES(NEW.id);
 ELSIF NEW.schedulable IS DISTINCT FROM OLD.schedulable OR
    NEW.deleted_at IS DISTINCT FROM OLD.deleted_at OR
    NEW.status IS DISTINCT FROM OLD.status OR
    NEW.type IS DISTINCT FROM OLD.type OR
    NEW.platform IS DISTINCT FROM OLD.platform OR
    NEW.expires_at IS DISTINCT FROM OLD.expires_at OR
    NEW.auto_pause_on_expired IS DISTINCT FROM OLD.auto_pause_on_expired OR
    NEW.credentials->'model_mapping' IS DISTINCT FROM OLD.credentials->'model_mapping' OR
    NEW.credentials->'model_mapping_mode' IS DISTINCT FROM OLD.credentials->'model_mapping_mode' OR
    NEW.credentials->'compact_model_mapping' IS DISTINCT FROM OLD.credentials->'compact_model_mapping' OR
    NEW.extra->'openai_passthrough' IS DISTINCT FROM OLD.extra->'openai_passthrough' OR
    NEW.credentials->'api_protocol' IS DISTINCT FROM OLD.credentials->'api_protocol' THEN
  UPDATE oauth_429_account_epochs SET epoch=epoch+1,
   valid_since=GREATEST(clock_timestamp(),valid_since+interval '1 microsecond') WHERE account_id=NEW.id;
  -- Clear observing/completed windows as well. Preserve run evidence for audit;
  -- a fresh observation resets it only after binding the new account epoch.
  UPDATE oauth_429_tasks SET state=CASE WHEN state IN ('pending','running') THEN 'cancelled' ELSE state END,
   data=jsonb_set(data,'{samples}','[]'),updated_at=clock_timestamp() WHERE account_id=NEW.id;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER oauth_429_account_epoch_insert AFTER INSERT ON accounts
 FOR EACH ROW EXECUTE FUNCTION cancel_oauth_429_account_tasks();
