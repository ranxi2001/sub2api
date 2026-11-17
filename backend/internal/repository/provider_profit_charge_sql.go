package repository

// All staging is private to the transaction, including on a pooled connection.
const providerProfitStageSQL = `
CREATE TEMP TABLE profit_sync_accounts (id bigint PRIMARY KEY) ON COMMIT DROP;
CREATE TEMP TABLE profit_sync_remote (
 usage_id bigint, key_id bigint, user_id bigint, hash text, at timestamptz,
 amount numeric, settled numeric, unique_at_site boolean
) ON COMMIT DROP`

const providerProfitStageRemoteSQL = `
INSERT INTO pg_temp.profit_sync_remote
 SELECT * FROM jsonb_to_recordset($1::jsonb) AS r(usage_id bigint,key_id bigint,user_id bigint,hash text,at timestamptz,amount numeric,settled numeric,unique_at_site boolean)`

// Materialize ownership and ambiguity once on the server, not once per batch.
// Count ties across BOTH clock-skew margins before selecting the actual window.
// Stage request hashes only; raw local request IDs stay in usage_logs.
const providerProfitStageLocalsSQL = `
CREATE TEMP TABLE profit_sync_locals ON COMMIT DROP AS
WITH candidates AS (
 SELECT u.id,u.account_id,u.created_at,u.upstream_request_id,s.key_id,own.channel_id,
        s.effective_from,s.effective_until,s.identity_from,s.origin_hash,own.assignment
 FROM provider_profit_account_source_history s
 JOIN usage_logs u ON u.account_id=s.account_id AND u.created_at>=s.effective_from
     AND (u.created_at<s.effective_until OR s.effective_until IS NULL)
 CROSS JOIN LATERAL provider_profit_account_at(u.account_id,u.created_at) own
 WHERE s.provider_id=$1 AND own.provider_id=s.provider_id AND own.key_id=s.key_id AND own.channel_id IS NOT NULL
   AND u.created_at >= $2::timestamptz - make_interval(secs=>$6)
   AND u.created_at < $3::timestamptz + make_interval(secs=>$6)
 UNION ALL
 -- A dashboard URL can nominate a previously hand-entered account, but
 -- is not proof of which key paid. Never override import history (including
 -- a key that has since been edited), a manual unlink, or a legacy binding.
 SELECT u.id,u.account_id,u.created_at,u.upstream_request_id,NULL::text,
        COALESCE(frozen.channel_id,own.channel_id,$7::bigint),NULL::timestamptz,NULL::timestamptz,$4::timestamptz,$5::text,own.assignment
 FROM pg_temp.profit_sync_accounts p JOIN accounts a ON a.id=p.id AND a.type='apikey' AND NOT (COALESCE(a.extra,'{}'::jsonb) ? 'provider_binding')
 JOIN usage_logs u ON u.account_id=a.id
 LEFT JOIN LATERAL provider_profit_account_at(u.account_id,u.created_at) own ON true
 LEFT JOIN provider_profit_charges frozen ON frozen.usage_log_id=u.id
 WHERE $7::bigint>0 AND u.created_at >= $2::timestamptz-make_interval(secs=>$6) AND u.created_at < $3::timestamptz+make_interval(secs=>$6)
   AND (own.assignment IS NULL OR (own.assignment='explicit' AND own.provider_id=$1))
   AND NOT EXISTS (SELECT 1 FROM provider_profit_account_source_history s WHERE s.account_id=a.id)
   AND NOT EXISTS (SELECT 1 FROM provider_profit_manual_history m WHERE m.account_id=a.id AND m.legacy_at_migration AND u.created_at<m.effective_from)
   AND (frozen.usage_log_id IS NULL OR (frozen.provider_id=$1 AND frozen.origin_hash=$5 AND frozen.identity_from=$4 AND frozen.account_id=u.account_id AND frozen.local_created_at=u.created_at))
), counted AS (
 SELECT c.*, count(*) OVER (PARTITION BY origin_hash,key_id,upstream_request_id) AS n
 FROM candidates c
)
SELECT id,account_id,channel_id,created_at,key_id,effective_from,effective_until,identity_from,origin_hash,assignment,n,
       encode(sha256(convert_to(NULLIF(upstream_request_id,''),'UTF8')),'hex') AS request_hash
FROM counted WHERE created_at >= $2 AND created_at < $3`

// Keyset-page all window-local rows (including unmatched ones). Remote and
// competing local evidence are never restricted to this page. The outer query
// returns one summary row to Go; all writes roll back on a later page's error.
const providerProfitChargeSQL = `
WITH locals AS MATERIALIZED (
 SELECT * FROM pg_temp.profit_sync_locals WHERE ($5::bigint IS NULL OR id>$5) ORDER BY id LIMIT $6
), eligible AS MATERIALIZED (
 SELECT l.id,l.account_id,l.channel_id,l.created_at,r.*
 FROM locals l JOIN pg_temp.profit_sync_remote r ON r.hash=l.request_hash
   AND (r.key_id::text=l.key_id OR (l.key_id IS NULL AND r.unique_at_site))
 JOIN usage_logs u ON u.id=l.id AND u.created_at=l.created_at
 LEFT JOIN LATERAL provider_profit_account_at(l.account_id,r.at) other ON true
 WHERE l.n=1 AND l.identity_from=$2 AND l.origin_hash=$3
   AND r.at BETWEEN l.created_at-make_interval(secs=>$4) AND l.created_at+make_interval(secs=>$4)
   AND ((l.key_id IS NOT NULL AND r.at>=$2 AND r.at>=l.effective_from AND (r.at<l.effective_until OR l.effective_until IS NULL)
         AND other.provider_id=$1 AND other.key_id=l.key_id AND other.channel_id=l.channel_id)
     OR (l.key_id IS NULL AND other.assignment IS NOT DISTINCT FROM l.assignment
         AND (other.assignment IS NULL OR (other.channel_id=l.channel_id AND other.provider_id=$1))
         -- Backfill before the FIRST observed wallet identity only. Logging in
         -- as another user at this site must not retrofit that wallet to history.
         AND (least(l.created_at,r.at)>=$2 OR NOT EXISTS (SELECT 1 FROM provider_profit_channel_history h WHERE h.provider_id=$1 AND h.identity_from<$2))
         AND NOT EXISTS (SELECT 1 FROM provider_profit_manual_history m WHERE m.account_id=l.account_id AND m.legacy_at_migration AND r.at<m.effective_from)))
   -- Another catalog entry can point at the same wallet. Manual unlinking
   -- also changes ownership, not the key that actually made a request.
   -- Use the origin recorded at import, including unverified wallet claims:
   -- another site cannot compete, and a later catalog edit cannot erase a tie.
   AND NOT EXISTS (
       SELECT 1 FROM usage_logs d LEFT JOIN provider_profit_account_source_history src
         ON src.account_id=d.account_id AND src.effective_from<=d.created_at AND (src.effective_until IS NULL OR d.created_at<src.effective_until)
       WHERE d.id<>l.id AND d.upstream_request_id=u.upstream_request_id
         AND d.created_at BETWEEN r.at-make_interval(secs=>$4) AND r.at+make_interval(secs=>$4)
         AND ((src.origin_hash=l.origin_hash AND src.key_id=r.key_id::text)
              OR (src.account_id IS NULL AND d.account_id IN (SELECT id FROM pg_temp.profit_sync_accounts))))
), same AS MATERIALIZED (
 SELECT e.id,e.settled,c.settled_amount
 FROM eligible e JOIN provider_profit_charges c ON c.usage_log_id=e.id
 WHERE (c.provider_id,c.identity_from,c.origin_hash,c.account_id,c.channel_id,c.local_created_at,
        c.upstream_usage_id,c.upstream_user_id,c.key_id,c.upstream_request_id_hash,c.upstream_created_at,c.amount)=
       ($1,$2,$3,e.account_id,e.channel_id,e.created_at,e.usage_id,e.user_id,e.key_id,e.hash,e.at,e.amount)
    AND (c.settled_amount IS NULL OR e.settled IS NULL OR c.settled_amount=e.settled)
), promoted AS (
 UPDATE provider_profit_charges c SET settled_amount=s.settled FROM same s WHERE c.usage_log_id=s.id
 AND s.settled IS NOT NULL AND s.settled_amount IS NULL
), inserted AS (
 INSERT INTO provider_profit_charges(usage_log_id,provider_id,account_id,channel_id,identity_from,origin_hash,
       upstream_usage_id,upstream_user_id,key_id,upstream_request_id_hash,local_created_at,upstream_created_at,amount,settled_amount)
 SELECT id,$1,account_id,channel_id,$2,$3,usage_id,user_id,key_id,hash,created_at,at,amount,settled FROM eligible e
 WHERE NOT EXISTS(SELECT 1 FROM provider_profit_charges c WHERE c.usage_log_id=e.id)
 ON CONFLICT DO NOTHING RETURNING usage_log_id
), totals AS (
 SELECT (SELECT count(*) FROM eligible) AS eligible,
        (SELECT count(*) FROM same) + (SELECT count(*) FROM inserted) AS matched
)
SELECT (SELECT max(id) FROM locals), (SELECT count(*) FROM locals), matched, eligible-matched FROM totals`

// One row per provider and requested window. Repeat syncs refresh the time
// and counts; they do not insert another receipt. Nothing here expires.
const providerProfitSyncRunSQL = `
INSERT INTO provider_profit_sync_runs(provider_id, window_from, window_until, matched, unmatched)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (provider_id, window_from, window_until)
DO UPDATE SET synced_at = clock_timestamp(), matched = EXCLUDED.matched, unmatched = EXCLUDED.unmatched`
