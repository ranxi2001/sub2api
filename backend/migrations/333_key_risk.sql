-- Independent security metadata: never store credential values or prompt bodies.
CREATE TABLE key_risk_control (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 config jsonb NOT NULL DEFAULT '{}',
 last_scan_at timestamptz,
 last_error text NOT NULL DEFAULT ''
);
INSERT INTO key_risk_control(singleton) VALUES(true);
CREATE TABLE key_risk_events (
 id bigserial PRIMARY KEY,
 rule text NOT NULL,
 subject text NOT NULL,
 evidence jsonb NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','ignored','resolved')),
 first_seen timestamptz NOT NULL,
 last_seen timestamptz NOT NULL,
 actor_id bigint,
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(rule,subject)
);
CREATE INDEX key_risk_events_latest ON key_risk_events(last_seen DESC,id DESC);
CREATE TABLE key_risk_outbox (
 id bigserial PRIMARY KEY,
 user_id bigint NOT NULL,
 kind text NOT NULL CHECK(kind IN ('age','risk')),
 bucket bigint NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','sending','sent','failed','uncertain','skipped')),
 manual boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 attempts integer NOT NULL DEFAULT 0,
 available_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(user_id,kind,bucket)
);
CREATE INDEX key_risk_outbox_pending ON key_risk_outbox(available_at) WHERE state='pending';
