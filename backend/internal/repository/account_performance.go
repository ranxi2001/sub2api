package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type accountPerformanceRepository struct{ db *sql.DB }

func NewAccountPerformanceRepository(db *sql.DB) service.AccountPerformanceRepository {
	return &accountPerformanceRepository{db: db}
}

// Strict, completed instrumented requests only. Missing traces/ambiguous traces,
// unsuccessful streams, media and unknown timings remain NULL, never zero.
// The trace start is the actual event time, not the asynchronous usage insert.
// Normalize Z and nanoseconds before jsonpath .datetime(): PostgreSQL 14-16
// needs an explicit UTC offset; all supported versions accept microseconds.
const performanceMetricsSQL = `
WITH source AS (
 SELECT u.*, t.detail,
  (jsonb_path_query_first_tz(jsonb_build_object('at',
   regexp_replace(regexp_replace(t.detail->>'started_at','Z$','+00:00'),
    '(\.[0-9]{6})[0-9]+([+-])','\1\2')),
   '$.at.datetime()', '{}',true)#>>'{}')::timestamptz AS event_at
 FROM usage_logs u
 JOIN LATERAL (
  SELECT (array_agg(detail))[1] AS detail FROM (
   SELECT detail FROM request_timing_details WHERE usage_log_id=u.id LIMIT 2
  ) bounded_traces HAVING COUNT(*)=1
 ) t ON true
 -- Successful usage is timestamped at completion, never before trace start.
 -- Reuses idx_usage_logs_account_created_at before joining timing evidence.
 WHERE u.account_id=ANY($1) AND u.created_at >= $2
), successful AS (
 SELECT * FROM source
 WHERE event_at >= $2 AND event_at <= $3
 AND detail->>'version'='1' AND detail->>'outcome'='success'
 AND detail->>'status'='200' AND detail->>'body_complete'='true'
 AND detail->>'canceled'='false' AND detail->>'truncated'='false'
 AND detail->>'client_disconnect'='false' AND detail->>'downstream_error'='false'
 AND (NOT stream OR detail->>'terminal'='completed')
 AND COALESCE(image_count,0)=0 AND COALESCE(image_output_tokens,0)=0 AND COALESCE(billing_mode,'token') NOT IN ('image','video')
), usage AS (
 SELECT account_id,
 AVG(output_tokens*1000.0/NULLIF(duration_ms,0)) FILTER(WHERE output_tokens>=2 AND duration_ms>0) AS tps,
 COUNT(*) FILTER(WHERE output_tokens>=2 AND duration_ms>0) AS tps_n,
 AVG(first_token_ms) FILTER(WHERE stream AND first_token_ms>=0 AND first_token_ms<=duration_ms AND output_tokens>=2) AS ttft,
 COUNT(*) FILTER(WHERE stream AND first_token_ms>=0 AND first_token_ms<=duration_ms AND output_tokens>=2) AS ttft_n
 FROM successful GROUP BY account_id
), rounds AS (
 SELECT p.account_id,p.id,r.quality_round_id,
 bool_or(r.quality_judgment->>'verdict'='incorrect' AND r.status='failed' AND r.error_message IN ('answer_mismatch','state_degraded')
    AND (COALESCE(r.pelican_config->>'question_kind','')<>'random_reasoning'
      OR r.pelican_config->'quality_batch'->>'decision'='peer_passed')) AS failed,
 bool_and(COALESCE(r.quality_judgment->>'verdict'='correct' AND r.status='success'
    AND (COALESCE(r.pelican_config->>'question_kind','')<>'random_reasoning'
      OR r.pelican_config->'quality_batch'->>'decision'='passed'),false)) AS passed
 FROM scheduled_test_plans p JOIN scheduled_test_results r ON r.plan_id=p.id
 WHERE p.account_id=ANY($1) AND p.enabled AND jsonb_typeof(p.pelican_config->'quality')='object'
 -- Probe records are created after execution. The indexed completion-time
 -- lower bound preserves the entire round (event-time HAVING below is final).
 AND r.created_at >= $2
 AND r.quality_round_id<>''
 GROUP BY p.account_id,p.id,r.quality_round_id
 HAVING MIN(r.started_at)>=$2 AND MAX(r.finished_at)<=$3 AND MIN(r.started_at)>=MAX(p.updated_at)
 AND bool_and(r.finished_at IS NOT NULL AND r.finished_at>=r.started_at)
 AND bool_and(r.quality_action NOT IN ('','stale_run','comparison_pending'))
 AND bool_and(COALESCE(r.pelican_config->>'parallel_count','1') ~ '^[1-8]$')
 AND bool_and(r.pelican_config->'model_ids' IS NULL OR
  (jsonb_typeof(r.pelican_config->'model_ids')='array'
   AND jsonb_array_length(CASE WHEN jsonb_typeof(r.pelican_config->'model_ids')='array' THEN r.pelican_config->'model_ids' ELSE '[]'::jsonb END)<=50))
 AND COUNT(*)=MAX(CASE WHEN COALESCE(r.pelican_config->>'parallel_count','1') ~ '^[1-8]$'
  THEN COALESCE(r.pelican_config->>'parallel_count','1')::int *
   GREATEST(1,jsonb_array_length(CASE WHEN jsonb_typeof(r.pelican_config->'model_ids')='array' THEN r.pelican_config->'model_ids' ELSE '[]'::jsonb END)) END)
 ), quality AS (
 SELECT account_id,100.0*COUNT(*) FILTER(WHERE failed)/NULLIF(COUNT(*) FILTER(WHERE passed OR failed),0) AS degraded,
 COUNT(*) FILTER(WHERE passed OR failed) AS quality_n FROM rounds GROUP BY account_id
)
SELECT ids.id,u.tps,COALESCE(u.tps_n,0),u.ttft,COALESCE(u.ttft_n,0),q.degraded,COALESCE(q.quality_n,0),
 COALESCE(m.config,'{}'),COALESCE(m.revision,0),COALESCE(m.paused,false),COALESCE(m.pause_generation,0),m.paused_at,COALESCE(m.reason,'')
FROM unnest($1::bigint[]) ids(id)
JOIN accounts a ON a.id=ids.id AND a.deleted_at IS NULL
LEFT JOIN usage u ON u.account_id=ids.id LEFT JOIN quality q ON q.account_id=ids.id
LEFT JOIN account_performance_monitors m ON m.account_id=ids.id
`

func readAccountPerformanceWindowMinutes(ctx context.Context, db sqlExecutor) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT value FROM settings WHERE key=$1`, service.SettingKeyAccountPerformanceWindowMinutes)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var value string
	if rows.Next() {
		if err := rows.Scan(&value); err != nil {
			return 0, err
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return service.ParseAccountPerformanceWindowMinutes(value)
}

func readAccountPerformance(ctx context.Context, db sqlExecutor, ids []int64, now time.Time) (map[int64]service.AccountPerformance, error) {
	out := make(map[int64]service.AccountPerformance, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	minutes, err := readAccountPerformanceWindowMinutes(ctx, db)
	if err != nil {
		return nil, err
	}
	since := now.Add(-time.Duration(minutes) * time.Minute)
	rows, err := db.QueryContext(ctx, performanceMetricsSQL, pq.Array(ids), since, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var m service.AccountPerformance
		var config []byte
		if err := rows.Scan(&m.AccountID, &m.AvgTPS, &m.TPSSamples, &m.AvgTTFTMs, &m.TTFTSamples, &m.DegradationPercent, &m.QualitySamples, &config, &m.Revision, &m.Paused, &m.PauseGeneration, &m.PausedAt, &m.Reason); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(config, &m.Config); err != nil {
			return nil, err
		}
		m.WindowStart, m.WindowEnd = since, now
		out[m.AccountID] = m
	}
	return out, rows.Err()
}
func (r *accountPerformanceRepository) Read(ctx context.Context, ids []int64, now time.Time) (map[int64]service.AccountPerformance, error) {
	out, err := readAccountPerformance(ctx, r.db, ids, now)
	if err != nil || len(out) == 0 {
		return out, err
	}
	var since time.Time
	for _, m := range out {
		since = m.WindowStart
		break
	}
	evidence := upstreamstats.Default.Snapshot(ids, since, now)
	for id, m := range out {
		m.UpstreamError = evidence[id]
		out[id] = m
	}
	return out, nil
}
func (r *accountPerformanceRepository) EnabledIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT m.account_id FROM account_performance_monitors m JOIN accounts a ON a.id=m.account_id WHERE m.config->>'enabled'='true' AND NOT m.paused AND a.deleted_at IS NULL ORDER BY m.account_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func performancePaused(ctx context.Context, db sqlExecutor, ids []int64) (map[int64]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT account_id FROM account_performance_monitors WHERE paused AND account_id=ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
func (r *accountPerformanceRepository) Paused(ctx context.Context, ids []int64) (map[int64]bool, error) {
	return performancePaused(ctx, r.db, ids)
}
func (r *accountRepository) PerformancePaused(ctx context.Context, ids []int64) (map[int64]bool, error) {
	return performancePaused(ctx, r.sql, ids)
}
func (r *accountPerformanceRepository) Save(ctx context.Context, id int64, c service.PerformanceConfig, revision, actor int64) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockPerformanceAccount(ctx, tx, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_performance_monitors(account_id) VALUES($1) ON CONFLICT DO NOTHING`, id)
	if err != nil {
		return err
	}
	var gen int64
	err = tx.QueryRowContext(ctx, `UPDATE account_performance_monitors SET config=$2,revision=revision+1,updated_at=NOW() WHERE account_id=$1 AND revision=$3 RETURNING pause_generation`, id, string(raw), revision).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrPerformanceConflict
	}
	if err != nil {
		return err
	}
	if err = performanceAudit(ctx, tx, id, "configure", actor, revision+1, gen, raw); err != nil {
		return err
	}
	return tx.Commit()
}
func performanceAudit(ctx context.Context, tx *sql.Tx, id int64, action string, actor, revision, gen int64, evidence []byte) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO account_performance_events(account_id,action,actor_id,revision,pause_generation,evidence) VALUES($1,$2,NULLIF($3::bigint,0),$4,$5,$6)`, id, action, actor, revision, gen, string(evidence))
	return err
}

// Soft deletion changes a non-key column: KEY SHARE would allow it through.
// All monitor mutations lock account -> monitor, holding the account active
// until the state change and audit commit together.
func lockPerformanceAccount(ctx context.Context, tx *sql.Tx, id int64) error {
	var existing int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrPerformanceConflict
	}
	return err
}

func (r *accountPerformanceRepository) Release(ctx context.Context, id, revision, generation, actor int64) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockPerformanceAccount(ctx, tx, id); err != nil {
		return err
	}
	// Explicit release disables evaluation atomically; no old evaluator can re-pause.
	res, err := tx.ExecContext(ctx, `UPDATE account_performance_monitors SET paused=false,paused_at=NULL,reason='',config=jsonb_set(config,'{enabled}','false'),revision=revision+1,updated_at=NOW() WHERE account_id=$1 AND revision=$2 AND pause_generation=$3 AND paused`, id, revision, generation)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrPerformanceConflict
	}
	if err = performanceAudit(ctx, tx, id, "release", actor, revision+1, generation, []byte(`{"enabled":false}`)); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *accountPerformanceRepository) Evaluate(ctx context.Context, id int64, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockPerformanceAccount(ctx, tx, id); err != nil {
		if errors.Is(err, service.ErrPerformanceConflict) {
			return nil // Deleted after EnabledIDs was captured.
		}
		return err
	}
	var raw []byte
	var revision, gen int64
	var paused bool
	// Multiple instances skip claimed rows. Configuration and release use the same lock.
	err = tx.QueryRowContext(ctx, `SELECT config,revision,paused,pause_generation FROM account_performance_monitors WHERE account_id=$1 FOR UPDATE SKIP LOCKED`, id).Scan(&raw, &revision, &paused, &gen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var cfg service.PerformanceConfig
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if !cfg.Enabled || paused {
		return nil
	}
	if err = cfg.Validate(); err != nil {
		return err
	}
	metrics, err := readAccountPerformance(ctx, tx, []int64{id}, now)
	if err != nil {
		return err
	}
	m := metrics[id]
	reasons := m.Breaches()
	if len(reasons) == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE account_performance_monitors SET paused=true,pause_generation=pause_generation+1,paused_at=$2,reason=$3,updated_at=NOW() WHERE account_id=$1`, id, now, strings.Join(reasons, ","))
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err = performanceAudit(ctx, tx, id, "pause", 0, revision, gen+1, evidence); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *accountPerformanceRepository) Events(ctx context.Context, id int64) ([]service.PerformanceEvent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,action,actor_id,revision,pause_generation,evidence,created_at FROM account_performance_events WHERE account_id=$1 ORDER BY id DESC LIMIT 50`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.PerformanceEvent, 0)
	for rows.Next() {
		var e service.PerformanceEvent
		if err := rows.Scan(&e.ID, &e.Action, &e.ActorID, &e.Revision, &e.PauseGeneration, &e.Evidence, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
