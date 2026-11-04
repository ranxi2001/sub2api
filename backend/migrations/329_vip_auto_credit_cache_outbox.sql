-- Separate from immutable migration 328 (first repository increment).
CREATE TABLE IF NOT EXISTS auto_credit_cache_outbox (
 id BIGINT PRIMARY KEY REFERENCES auto_credit_ledger(id),
 user_id BIGINT NOT NULL REFERENCES users(id),
 stage INTEGER NOT NULL DEFAULT 0 CHECK(stage IN (0,1)),
 available_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS auto_credit_cache_outbox_available ON auto_credit_cache_outbox(available_at,id);
