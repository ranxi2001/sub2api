-- Durable, administrator-only receipts for completed transfers. Confirmation
-- tokens are deliberately excluded; old short-lived requests are not fabricated.
CREATE TABLE IF NOT EXISTS qq_bot_transfer_history (
 id BIGSERIAL PRIMARY KEY,
 app_id TEXT NOT NULL,
 from_member_id TEXT NOT NULL,
 from_user_id BIGINT NOT NULL,
 to_user_id BIGINT NOT NULL,
 from_email TEXT NOT NULL,
 to_email TEXT NOT NULL,
 amount NUMERIC(18,8) NOT NULL CHECK(amount > 0),
 fee NUMERIC(18,8) NOT NULL CHECK(fee >= 0),
 net_amount NUMERIC(18,8) NOT NULL CHECK(net_amount > 0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 CHECK(amount = fee + net_amount)
);
CREATE INDEX IF NOT EXISTS qq_bot_transfer_history_app_time ON qq_bot_transfer_history(app_id,created_at DESC,id DESC);
