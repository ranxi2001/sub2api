package repository

// This statement reads a single ownership/usage snapshot, and writes only the
// evidence table. Scan a clock-skew margin on BOTH sides so narrowing a window
// cannot pick one of two local candidates for a single upstream event.
const providerProfitChargeSQL = `
WITH candidates AS MATERIALIZED (
 SELECT u.id,u.account_id,u.created_at,u.upstream_request_id,s.key_id,own.channel_id,
        s.effective_from,s.effective_until,s.identity_from,s.origin_hash
 FROM provider_profit_account_source_history s
 JOIN usage_logs u ON u.account_id=s.account_id AND u.created_at>=s.effective_from
     AND (u.created_at<s.effective_until OR s.effective_until IS NULL)
 CROSS JOIN LATERAL provider_profit_account_at(u.account_id,u.created_at) own
 WHERE s.provider_id=$1 AND own.provider_id=s.provider_id AND own.key_id=s.key_id AND own.channel_id IS NOT NULL
   AND u.created_at >= $2::timestamptz - make_interval(secs=>$8)
   AND u.created_at < $3::timestamptz + make_interval(secs=>$8)
 LIMIT $7
), locals AS (
 SELECT c.*, count(*) OVER (PARTITION BY origin_hash,key_id,upstream_request_id) AS n
 FROM candidates c
), remote AS (
 SELECT * FROM jsonb_to_recordset($6::jsonb) AS r(usage_id bigint,key_id bigint,user_id bigint,hash text,at timestamptz,amount numeric,settled numeric)
), eligible AS MATERIALIZED (
 SELECT l.id,l.account_id,l.channel_id,l.created_at,r.*
 FROM locals l JOIN remote r ON r.key_id::text=l.key_id AND r.hash=encode(sha256(convert_to(l.upstream_request_id,'UTF8')),'hex')
 CROSS JOIN LATERAL provider_profit_account_at(l.account_id,r.at) other
 WHERE l.n=1 AND l.upstream_request_id<>'' AND l.identity_from=$4 AND l.origin_hash=$5
   AND l.created_at >= $2 AND l.created_at < $3 AND r.at >= $2 AND r.at < $3
   AND r.at BETWEEN l.created_at-make_interval(secs=>$8) AND l.created_at+make_interval(secs=>$8)
   AND r.at>=l.effective_from AND (r.at<l.effective_until OR l.effective_until IS NULL)
   AND other.provider_id=$1 AND other.key_id=l.key_id AND other.channel_id=l.channel_id
   -- Another catalog entry can point at the same wallet. Manual unlinking
   -- also changes ownership, not the key that actually made a request.
   -- Use the origin recorded at import, including unverified wallet claims:
   -- another site cannot compete, and a later catalog edit cannot erase a tie.
   AND NOT EXISTS (
       SELECT 1 FROM usage_logs d JOIN provider_profit_account_source_history src
         ON src.account_id=d.account_id AND src.origin_hash=l.origin_hash AND src.key_id=l.key_id AND src.effective_from<=d.created_at
            AND (src.effective_until IS NULL OR d.created_at<src.effective_until)
       WHERE d.id<>l.id AND d.upstream_request_id=l.upstream_request_id
         AND d.created_at BETWEEN r.at-make_interval(secs=>$8) AND r.at+make_interval(secs=>$8))
), same AS MATERIALIZED (
 SELECT e.id,e.settled,c.settled_amount
 FROM eligible e JOIN provider_profit_charges c ON c.usage_log_id=e.id
 WHERE (c.provider_id,c.identity_from,c.origin_hash,c.account_id,c.channel_id,c.local_created_at,
        c.upstream_usage_id,c.upstream_user_id,c.key_id,c.upstream_request_id_hash,c.upstream_created_at,c.amount)=
       ($1,$4,$5,e.account_id,e.channel_id,e.created_at,e.usage_id,e.user_id,e.key_id,e.hash,e.at,e.amount)
    AND (c.settled_amount IS NULL OR e.settled IS NULL OR c.settled_amount=e.settled)
), promoted AS (
 UPDATE provider_profit_charges c SET settled_amount=s.settled FROM same s WHERE c.usage_log_id=s.id
 AND s.settled IS NOT NULL AND s.settled_amount IS NULL
), inserted AS (
 INSERT INTO provider_profit_charges(usage_log_id,provider_id,account_id,channel_id,identity_from,origin_hash,
       upstream_usage_id,upstream_user_id,key_id,upstream_request_id_hash,local_created_at,upstream_created_at,amount,settled_amount)
 SELECT id,$1,account_id,channel_id,$4,$5,usage_id,user_id,key_id,hash,created_at,at,amount,settled FROM eligible e
 WHERE NOT EXISTS(SELECT 1 FROM provider_profit_charges c WHERE c.usage_log_id=e.id)
 ON CONFLICT DO NOTHING RETURNING usage_log_id
), totals AS (
 SELECT (SELECT count(*) FROM eligible) AS eligible,
        (SELECT count(*) FROM same) + (SELECT count(*) FROM inserted) AS matched
)
SELECT (SELECT count(*) FROM candidates), matched,
       (SELECT count(*) FROM locals WHERE created_at >= $2 AND created_at < $3) - matched,
       eligible - matched FROM totals`
