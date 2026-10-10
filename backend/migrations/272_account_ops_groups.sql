-- Use a durable upstream account-group key while retaining the triggering local account.
ALTER TABLE account_ops_alerts ADD COLUMN IF NOT EXISTS group_id TEXT NOT NULL DEFAULT '';
ALTER TABLE account_ops_alerts ADD COLUMN IF NOT EXISTS group_name TEXT NOT NULL DEFAULT '';
UPDATE account_ops_alerts SET group_id='account:'||account_id::text WHERE group_id='';
UPDATE account_ops_alerts SET group_name=account_name WHERE group_name='';
ALTER TABLE account_ops_alerts DROP CONSTRAINT IF EXISTS account_ops_alerts_pkey;
ALTER TABLE account_ops_alerts ADD CONSTRAINT account_ops_alerts_pkey PRIMARY KEY(group_id,kind);
CREATE INDEX IF NOT EXISTS account_ops_alerts_account ON account_ops_alerts(account_id,kind);

ALTER TABLE account_ops_threshold_monitors ADD COLUMN IF NOT EXISTS group_id TEXT NOT NULL DEFAULT '';
UPDATE account_ops_threshold_monitors SET group_id='account:'||account_id::text WHERE group_id='';
ALTER TABLE account_ops_threshold_monitors DROP CONSTRAINT IF EXISTS account_ops_threshold_monitors_pkey;
ALTER TABLE account_ops_threshold_monitors ADD CONSTRAINT account_ops_threshold_monitors_pkey PRIMARY KEY(group_id,kind);
CREATE INDEX IF NOT EXISTS account_ops_threshold_monitors_account ON account_ops_threshold_monitors(account_id,kind);

ALTER TABLE account_ops_threshold_events ADD COLUMN IF NOT EXISTS group_id TEXT NOT NULL DEFAULT '';
ALTER TABLE account_ops_threshold_events ADD COLUMN IF NOT EXISTS group_name TEXT NOT NULL DEFAULT '';
UPDATE account_ops_threshold_events SET group_id='account:'||account_id::text WHERE group_id='';
UPDATE account_ops_threshold_events SET group_name=account_name WHERE group_name='';
CREATE INDEX IF NOT EXISTS account_ops_threshold_events_group ON account_ops_threshold_events(group_id,kind,last_seen DESC);
