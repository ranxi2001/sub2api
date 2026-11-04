-- Notification state only; no changes to balances or settled receipts.
CREATE TABLE balance_forecast_episodes (
 user_id bigint PRIMARY KEY REFERENCES users(id),
 generation bigint NOT NULL DEFAULT 1,
 active boolean NOT NULL DEFAULT true,
 healthy_since timestamptz,
 healthy_last_seen timestamptz,
 last_attempt_at timestamptz,
 sequence bigint NOT NULL DEFAULT 0
);
CREATE TABLE balance_forecast_outbox (
 id bigserial PRIMARY KEY,
 user_id bigint NOT NULL REFERENCES users(id),
 generation bigint NOT NULL,
 sequence bigint NOT NULL,
 state text NOT NULL CHECK (state IN ('sending','sent','uncertain','skipped')),
 created_at timestamptz NOT NULL,
 finished_at timestamptz,
 UNIQUE(user_id,generation,sequence)
);
CREATE INDEX balance_forecast_outbox_episode ON balance_forecast_outbox(user_id,generation,state);
