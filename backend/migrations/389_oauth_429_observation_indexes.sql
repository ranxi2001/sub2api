CREATE INDEX oauth_429_idle_idx ON oauth_429_tasks(updated_at)
 WHERE state NOT IN ('pending','running');
CREATE INDEX oauth_429_completed_model_idx ON account_auto_config_events
 (account_id, (details->'oauth429'->>'model'), (details->'oauth429'->>'revision'), id DESC)
 WHERE kind='oauth429_review_completed';
