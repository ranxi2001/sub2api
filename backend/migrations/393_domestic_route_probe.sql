-- The claim timestamp is independent of last_used_at and scheduler snapshots.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS domestic_route_probe_at timestamptz;

CREATE INDEX IF NOT EXISTS accounts_domestic_route_probe_due
    ON accounts (domestic_route_probe_at NULLS FIRST, id)
    WHERE deleted_at IS NULL AND extra @> '{"domestic_route_auto_schedule":true}';

-- Invalidate an in-flight decision when an administrator edits its inputs.
CREATE OR REPLACE FUNCTION fence_domestic_route_probe() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.extra->'domestic_route_auto_schedule' IS DISTINCT FROM OLD.extra->'domestic_route_auto_schedule' THEN
        NEW.domestic_route_probe_at := NULL;
    ELSIF OLD.domestic_route_probe_at IS NOT NULL AND (
        NEW.credentials IS DISTINCT FROM OLD.credentials OR NEW.proxy_id IS DISTINCT FROM OLD.proxy_id OR
        NEW.platform IS DISTINCT FROM OLD.platform OR NEW.type IS DISTINCT FROM OLD.type OR
        NEW.schedulable IS DISTINCT FROM OLD.schedulable OR NEW.status IS DISTINCT FROM OLD.status
    ) THEN
        NEW.domestic_route_probe_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS accounts_domestic_route_probe_fence ON accounts;
CREATE TRIGGER accounts_domestic_route_probe_fence
    BEFORE UPDATE OF extra, credentials, proxy_id, platform, type, schedulable, status ON accounts
    FOR EACH ROW EXECUTE FUNCTION fence_domestic_route_probe();
