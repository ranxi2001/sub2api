-- Independent ownership: no writes to accounts.schedulable, extra or cooldowns.
CREATE TABLE IF NOT EXISTS account_performance_monitors (
 account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 config JSONB NOT NULL DEFAULT '{"enabled":false,"tps":{},"degradation":{},"ttft":{}}',
 revision BIGINT NOT NULL DEFAULT 0,
 paused BOOLEAN NOT NULL DEFAULT false,
 pause_generation BIGINT NOT NULL DEFAULT 0,
 paused_at TIMESTAMPTZ,
 reason TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS account_performance_paused_idx ON account_performance_monitors(account_id) WHERE paused;
CREATE TABLE IF NOT EXISTS account_performance_events (
 id BIGSERIAL PRIMARY KEY,
 account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 action TEXT NOT NULL CHECK(action IN ('configure','pause','release')),
 actor_id BIGINT,
 revision BIGINT NOT NULL,
 pause_generation BIGINT NOT NULL,
 evidence JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS account_performance_events_account_idx ON account_performance_events(account_id,id DESC);
