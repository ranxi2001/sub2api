-- Run only after the actual application has completed its normal migrations.
-- SQL initializes fictional entities only: no plans, questions or result rows.
BEGIN;
INSERT INTO groups (name, platform, status)
VALUES ('fixture-pelican-group', 'anthropic', 'active');

INSERT INTO accounts (name, platform, type, credentials, concurrency, priority, status, schedulable)
SELECT 'fixture-pelican-' || alias, 'anthropic', 'apikey',
       jsonb_build_object('api_key', 'fixture-only-key-' || alias,
                          'base_url', 'https://fake-upstream:4199'),
       4, 50, 'active', true
FROM (VALUES ('a'), ('b')) AS fixtures(alias);

INSERT INTO account_groups (account_id, group_id)
SELECT a.id, g.id
FROM accounts a CROSS JOIN groups g
WHERE a.name IN ('fixture-pelican-a', 'fixture-pelican-b')
  AND g.name = 'fixture-pelican-group';
COMMIT;
