-- Display-only request snapshot. NULL means no historical Standard quote is
-- available; deliberately do not backfill from current group configuration.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS customer_cost_details JSONB;
COMMENT ON COLUMN usage_logs.customer_cost_details IS
    'Immutable customer Standard cost components when Free Fast actually applied; never used for billing';
