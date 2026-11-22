-- The row shape is shared by the account usage dialog and the profit report.
-- Index time first for reports and account first for the account dialog.
CREATE TABLE account_usage_financial_rollups (
    bucket timestamptz NOT NULL,
    account_id bigint NOT NULL,
    group_id bigint,
    model text NOT NULL,
    public_model text NOT NULL,
    upstream_model text,
    requests bigint NOT NULL,
    tokens numeric NOT NULL,
    standard_charge numeric NOT NULL,
    user_charge numeric NOT NULL,
    account_charge numeric NOT NULL,
    duration_sum numeric NOT NULL,
    duration_count bigint NOT NULL
);
CREATE INDEX account_usage_financial_time ON account_usage_financial_rollups(bucket);
CREATE INDEX account_usage_financial_account ON account_usage_financial_rollups(account_id,bucket);

CREATE TABLE account_usage_financial_pending (bucket timestamptz PRIMARY KEY);

CREATE FUNCTION account_usage_bucket(timestamptz) RETURNS timestamptz
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT to_timestamp(floor(extract(epoch FROM $1) / 300)::bigint * 300)
$$;

-- Transition tables coalesce a batch into one notification per time slice.
-- A shared writer lock pairs with the worker's exclusive lock. A late writer
-- either commits before the worker reads or queues the slice again afterwards.
CREATE FUNCTION queue_account_usage_financial() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    b timestamptz;
    source text;
BEGIN
    IF TG_OP = 'INSERT' THEN
        source := 'SELECT account_usage_bucket(created_at) FROM added';
    ELSIF TG_OP = 'DELETE' THEN
        source := 'SELECT account_usage_bucket(created_at) FROM removed';
    ELSE
        source := 'SELECT account_usage_bucket(created_at) FROM added UNION ALL SELECT account_usage_bucket(created_at) FROM removed';
    END IF;
    FOR b IN EXECUTE 'SELECT DISTINCT * FROM (' || source || ') bins ORDER BY 1' LOOP
        PERFORM pg_advisory_xact_lock_shared(825393, (extract(epoch FROM b)/300)::integer);
        INSERT INTO account_usage_financial_pending VALUES(b) ON CONFLICT DO NOTHING;
    END LOOP;
    RETURN NULL;
END;
$$;
CREATE TRIGGER account_usage_financial_insert AFTER INSERT ON usage_logs
REFERENCING NEW TABLE AS added FOR EACH STATEMENT EXECUTE FUNCTION queue_account_usage_financial();
CREATE TRIGGER account_usage_financial_update AFTER UPDATE ON usage_logs
REFERENCING OLD TABLE AS removed NEW TABLE AS added FOR EACH STATEMENT EXECUTE FUNCTION queue_account_usage_financial();
CREATE TRIGGER account_usage_financial_delete AFTER DELETE ON usage_logs
REFERENCING OLD TABLE AS removed FOR EACH STATEMENT EXECUTE FUNCTION queue_account_usage_financial();

-- The migration seeds work using the time index. Each worker transaction reads
-- just one five-minute slice, including on the first installation.
INSERT INTO account_usage_financial_pending
SELECT generate_series(account_usage_bucket((SELECT min(created_at) FROM usage_logs)),
                       account_usage_bucket(now()),interval '5 minutes') ON CONFLICT DO NOTHING;

CREATE VIEW account_usage_financial_events AS
SELECT created_at, account_id, group_id, model, COALESCE(NULLIF(requested_model,''),model) AS public_model, upstream_model,
    1::bigint AS requests,(input_tokens::bigint+output_tokens+cache_creation_tokens+cache_read_tokens)::numeric AS tokens,
    total_cost AS standard_charge,actual_cost AS user_charge,
    COALESCE(account_stats_cost,total_cost)*COALESCE(account_rate_multiplier,1) AS account_charge,
    COALESCE(duration_ms,0)::numeric AS duration_sum,(duration_ms IS NOT NULL)::int AS duration_count
FROM usage_logs;
