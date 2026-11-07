-- Preserve notification attribution independently of the rolling risk window.
ALTER TABLE key_risk_events ADD COLUMN generation bigint NOT NULL DEFAULT 1;
CREATE TABLE key_risk_outbox_items (
 outbox_id bigint NOT NULL REFERENCES key_risk_outbox(id) ON DELETE CASCADE,
 event_id bigint NOT NULL,
 generation bigint NOT NULL,
 key_id bigint NOT NULL,
 evidence jsonb NOT NULL,
 PRIMARY KEY(outbox_id,event_id,generation,key_id)
);
-- Upgrade pending notices with the evidence still available at migration time.
INSERT INTO key_risk_outbox_items(outbox_id,event_id,generation,key_id,evidence)
SELECT o.id,e.id,e.generation,k.id,e.evidence
FROM key_risk_outbox o JOIN api_keys k ON k.user_id=o.user_id
JOIN key_risk_events e ON e.evidence->'key_ids' @> to_jsonb(k.id)
WHERE o.kind='risk' AND o.state='pending' AND e.state='pending';
