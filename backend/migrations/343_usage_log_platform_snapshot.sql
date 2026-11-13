-- Immutable request-time provider identity. Legacy rows remain NULL: joining
-- current accounts/groups cannot recover historical platform accurately.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS platform VARCHAR(50);
COMMENT ON COLUMN usage_logs.platform IS 'Concrete upstream platform captured at request time; NULL means no historical snapshot';
