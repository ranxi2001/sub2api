-- Existing links keep their accounting basis. Naming a new upstream never opts into a wallet.
ALTER TABLE provider_profit_links ADD COLUMN cost_mode text NOT NULL DEFAULT 'wallet'
    CHECK (cost_mode IN ('estimate', 'wallet'));
ALTER TABLE provider_profit_links ALTER COLUMN cost_mode SET DEFAULT 'estimate';
