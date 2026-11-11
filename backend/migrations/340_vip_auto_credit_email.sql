-- Forward-only, opt-in for both existing and new configurations. Do not
-- backfill receipts for historic credits: they were issued without consent.
ALTER TABLE auto_credit_config ADD COLUMN IF NOT EXISTS notify_email BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS auto_credit_email_outbox (
    ledger_id BIGINT PRIMARY KEY REFERENCES auto_credit_ledger(id),
    recipient_email TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS auto_credit_email_outbox_pending
    ON auto_credit_email_outbox(available_at, ledger_id) WHERE sent_at IS NULL;
