-- Retire shared-exit heuristics without deleting their audit/delivery history.
UPDATE key_risk_events SET state='resolved',updated_at=now()
WHERE rule IN ('cross_user_ip','key_ip_spread_linked') AND state='pending';

-- The old default (5) becomes 2; preserve other explicitly configured thresholds.
UPDATE key_risk_control SET config=jsonb_set(config-'users_per_ip','{ips_per_key}',
 CASE WHEN config->>'ips_per_key' IS NULL OR config->>'ips_per_key'='5'
 THEN '2'::jsonb ELSE config->'ips_per_key' END);

-- Mixed queues retain valid key-specific items. Never retry in-flight mail here.
UPDATE key_risk_outbox o SET state='skipped',updated_at=now()
WHERE o.kind='risk' AND o.state='pending' AND NOT EXISTS (
 SELECT 1 FROM key_risk_outbox_items i JOIN key_risk_events e
 ON e.id=i.event_id AND e.generation=i.generation
 WHERE i.outbox_id=o.id AND e.rule='key_ip_spread' AND e.state='pending'
);
