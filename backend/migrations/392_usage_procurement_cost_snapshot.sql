-- SQL-owned usage metadata, like account_stats_cost. Historical rows remain NULL;
-- never backfill a request's procurement estimate from mutable account settings.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS procurement_cost_snapshot JSONB;

CREATE OR REPLACE FUNCTION reject_usage_procurement_cost_rewrite() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.procurement_cost_snapshot IS DISTINCT FROM NEW.procurement_cost_snapshot THEN
        RAISE EXCEPTION 'usage procurement cost snapshot is immutable';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS usage_procurement_cost_immutable ON usage_logs;
CREATE TRIGGER usage_procurement_cost_immutable
BEFORE UPDATE OF procurement_cost_snapshot ON usage_logs
FOR EACH ROW EXECUTE FUNCTION reject_usage_procurement_cost_rewrite();
