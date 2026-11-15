-- Daily settlement freezes eligibility; only an explicit welfare claim grants
-- the reward. Existing automatic grants and settled dates remain untouched.
CREATE TABLE IF NOT EXISTS qq_bot_ranking_claims (
    id BIGSERIAL PRIMARY KEY,
    app_id TEXT NOT NULL,
    ranking_day DATE NOT NULL,
    ranking_kind TEXT NOT NULL CHECK (ranking_kind IN ('tokens','amount')),
    rank INTEGER NOT NULL CHECK (rank BETWEEN 1 AND 10),
    user_id BIGINT NOT NULL REFERENCES users(id),
    source_key TEXT NOT NULL UNIQUE,
    reward JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claimed_at TIMESTAMPTZ,
    UNIQUE(app_id,ranking_day,ranking_kind,user_id)
);
CREATE INDEX IF NOT EXISTS idx_qq_bot_ranking_claims_user
    ON qq_bot_ranking_claims(app_id,user_id,ranking_day DESC,id DESC);
