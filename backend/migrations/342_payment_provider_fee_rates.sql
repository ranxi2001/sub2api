-- NULL preserves the existing site-wide fee for every existing instance.
ALTER TABLE payment_provider_instances
    ADD COLUMN IF NOT EXISTS fee_rate DECIMAL(5,2) NULL,
    ADD COLUMN IF NOT EXISTS method_fee_rates JSONB NOT NULL DEFAULT '{}'::jsonb;
