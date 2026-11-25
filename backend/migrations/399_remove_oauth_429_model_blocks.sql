-- Remove obsolete per-model review blocks, including entries on archived accounts.
WITH changed AS (
    UPDATE accounts
    SET extra = extra - 'oauth_429_model_blocks',
        updated_at = GREATEST(clock_timestamp(), updated_at + INTERVAL '1 microsecond')
    WHERE extra ? 'oauth_429_model_blocks'
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id)
SELECT 'account_changed', id FROM changed;
