-- Keep user acknowledgements separate from the administrator's risk disposition.
CREATE TABLE key_risk_recipients (
 event_id bigint NOT NULL REFERENCES key_risk_events(id) ON DELETE CASCADE,
 generation bigint NOT NULL,
 user_id bigint NOT NULL,
 key_ids bigint[] NOT NULL,
 acknowledged_at timestamptz,
 acknowledgement jsonb,
 PRIMARY KEY(event_id,generation,user_id)
);
CREATE INDEX key_risk_recipients_user ON key_risk_recipients(user_id,event_id DESC);
-- Independent of outbox retention; sending means an attempt, never proof of receipt/read.
CREATE TABLE key_risk_deliveries (
 outbox_id bigint NOT NULL,
 event_id bigint NOT NULL,
 generation bigint NOT NULL,
 user_id bigint NOT NULL,
 key_ids bigint[] NOT NULL,
 attempted_at timestamptz NOT NULL DEFAULT now(),
 message_version integer NOT NULL DEFAULT 1,
 state text NOT NULL DEFAULT 'sending' CHECK(state IN ('sending','sent','uncertain')),
 PRIMARY KEY(outbox_id,event_id,generation,user_id),
 FOREIGN KEY(event_id,generation,user_id) REFERENCES key_risk_recipients ON DELETE CASCADE
);
INSERT INTO key_risk_recipients(event_id,generation,user_id,key_ids)
SELECT e.id,e.generation,k.user_id,array_agg(k.id ORDER BY k.id)
FROM key_risk_events e JOIN api_keys k ON e.evidence->'key_ids' @> to_jsonb(k.id)
GROUP BY e.id,e.generation,k.user_id;
INSERT INTO key_risk_recipients(event_id,generation,user_id,key_ids)
SELECT i.event_id,i.generation,o.user_id,array_agg(DISTINCT i.key_id)
FROM key_risk_outbox_items i JOIN key_risk_outbox o ON o.id=i.outbox_id
JOIN key_risk_events e ON e.id=i.event_id
GROUP BY i.event_id,i.generation,o.user_id
ON CONFLICT DO NOTHING;
INSERT INTO key_risk_deliveries(outbox_id,event_id,generation,user_id,key_ids,attempted_at,state)
SELECT o.id,i.event_id,i.generation,o.user_id,array_agg(DISTINCT i.key_id),o.updated_at,o.state
FROM key_risk_outbox o JOIN key_risk_outbox_items i ON i.outbox_id=o.id
JOIN key_risk_recipients r ON r.event_id=i.event_id AND r.generation=i.generation AND r.user_id=o.user_id
WHERE o.state IN ('sending','sent','uncertain')
GROUP BY o.id,i.event_id,i.generation;
CREATE INDEX key_risk_deliveries_recipient ON key_risk_deliveries(event_id,generation,user_id);
CREATE INDEX key_risk_deliveries_user_time ON key_risk_deliveries(user_id,attempted_at DESC);
