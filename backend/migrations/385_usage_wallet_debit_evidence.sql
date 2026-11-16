-- New quoted charges only. A historical quote or quota counter does not prove a debit.
-- These two values are written by the successful balance transaction, never backfilled.
ALTER TABLE usage_billing_quotes
    ADD COLUMN debited_user_id BIGINT,
    ADD COLUMN wallet_debit NUMERIC(20,8),
    ADD CONSTRAINT usage_quote_wallet_debit_valid CHECK (
        (debited_user_id IS NULL AND wallet_debit IS NULL) OR
        (debited_user_id IS NOT NULL AND wallet_debit IS NOT NULL AND wallet_debit > 0 AND wallet_debit < 'Infinity'::numeric)
    );
