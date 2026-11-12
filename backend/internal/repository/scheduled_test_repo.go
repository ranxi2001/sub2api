package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// --- Plan Repository ---

type scheduledTestPlanRepository struct {
	db *sql.DB
}

func NewScheduledTestPlanRepository(db *sql.DB) service.ScheduledTestPlanRepository {
	return &scheduledTestPlanRepository{db: db}
}

func (r *scheduledTestPlanRepository) Create(ctx context.Context, plan *service.ScheduledTestPlan) (*service.ScheduledTestPlan, error) {
	return insertScheduledTestPlan(ctx, r.db, plan)
}

type planRowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// insertScheduledTestPlan is shared with group rules, which insert inside their own transaction.
func insertScheduledTestPlan(ctx context.Context, q planRowQuerier, plan *service.ScheduledTestPlan) (*service.ScheduledTestPlan, error) {
	if err := validateQualityGroupReferences(ctx, q, plan.PelicanConfig); err != nil {
		return nil, err
	}
	row := q.QueryRowContext(ctx, `
		INSERT INTO scheduled_test_plans (account_id, model_id, cron_expression, enabled, max_results, auto_recover, next_run_at, created_at, updated_at, pelican_config)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW(), $8)
		RETURNING id, account_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at, pelican_config, running_until
	`, plan.AccountID, plan.ModelID, plan.CronExpression, plan.Enabled, plan.MaxResults, plan.AutoRecover, plan.NextRunAt, marshalPelicanConfig(plan.PelicanConfig))
	return scanPlan(row)
}

func (r *scheduledTestPlanRepository) GetByID(ctx context.Context, id int64) (*service.ScheduledTestPlan, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, account_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at, pelican_config, running_until
		FROM scheduled_test_plans WHERE id = $1
	`, id)
	return scanPlan(row)
}

func (r *scheduledTestPlanRepository) ListByAccountID(ctx context.Context, accountID int64) ([]*service.ScheduledTestPlan, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at, pelican_config, running_until
		FROM scheduled_test_plans WHERE account_id = $1
		ORDER BY created_at DESC, id DESC
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPlans(rows)
}

func (r *scheduledTestPlanRepository) ListDue(ctx context.Context, now time.Time) ([]*service.ScheduledTestPlan, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at, pelican_config, running_until
		FROM scheduled_test_plans
		WHERE enabled = true AND next_run_at <= $1
		ORDER BY next_run_at ASC
	`, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPlans(rows)
}

func (r *scheduledTestPlanRepository) Update(ctx context.Context, plan *service.ScheduledTestPlan) (*service.ScheduledTestPlan, error) {
	const query = `
		UPDATE scheduled_test_plans
		SET model_id = $2, cron_expression = $3, enabled = $4, max_results = $5, auto_recover = $6, next_run_at = $7, updated_at = NOW(), pelican_config = $8
		WHERE id = $1 AND COALESCE(pelican_config->'quality'->>'action','') <> 'openai_two_fa_admission'
		RETURNING id, account_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at, pelican_config, running_until
	`
	args := []any{plan.ID, plan.ModelID, plan.CronExpression, plan.Enabled, plan.MaxResults, plan.AutoRecover, plan.NextRunAt, marshalPelicanConfig(plan.PelicanConfig)}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	updated, err := scanPlan(tx.QueryRowContext(ctx, query, args...))
	if err != nil {
		return nil, err
	}
	state, err := readQualityState(ctx, tx, plan.ID)
	if err != nil {
		return nil, err
	}
	// Importing configuration cannot implicitly undo account restrictions or
	// discard their ownership. Incompatible live isolation still rejects below.
	if !plan.PreserveQualityState && updated.PelicanConfig != nil && updated.PelicanConfig.Quality != nil &&
		updated.PelicanConfig.Quality.Action == service.QualityActionIsolateModels &&
		(state.Action == "remove_groups" || state.Action == "disable_scheduling") {
		if err = restoreQualityAccountActionForIsolation(ctx, tx, updated, &state); err != nil {
			return nil, err
		}
	}
	if err = checkQualityIsolationScope(updated, &state); err != nil {
		return nil, err
	}
	if hasQualityCooldownOwnership(&state) {
		account := &service.Account{}
		var credentials, extra []byte
		// Use the persisted account ID, never the caller's copy. This follows
		// detection's plan -> account lock order and fences concurrent remapping.
		if err = tx.QueryRowContext(ctx, `SELECT platform,type,COALESCE(credentials,'{}'::jsonb),COALESCE(extra,'{}'::jsonb) FROM accounts WHERE id=$1 FOR UPDATE`, updated.AccountID).Scan(&account.Platform, &account.Type, &credentials, &extra); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(credentials, &account.Credentials); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(extra, &account.Extra); err != nil {
			return nil, err
		}
		if err = checkQualityCooldownTargets(updated, account, &state); err != nil {
			return nil, err
		}
	}
	if err = validateQualityGroupReferences(ctx, tx, updated.PelicanConfig); err != nil {
		return nil, err
	}
	if !plan.PreserveQualityState && updated.PelicanConfig != nil && updated.PelicanConfig.Quality != nil && updated.PelicanConfig.Quality.Action == service.QualityActionObserveOnly {
		if _, err = tx.ExecContext(ctx, `DELETE FROM account_quality_states WHERE plan_id=$1`, plan.ID); err != nil {
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (r *scheduledTestPlanRepository) Delete(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var locked int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM scheduled_test_plans WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	if err := deleteScheduledTestPlanTx(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// The caller holds the plan lock. Manual and automated deletion share exactly
// the same ownership checks and never restore or modify the account.
func deleteScheduledTestPlanTx(ctx context.Context, tx *sql.Tx, id int64) error {
	state, err := readQualityState(ctx, tx, id)
	if err != nil {
		return err
	}
	if state.Action == service.QualityActionIsolateModels || state.Models != nil || state.Groups != nil {
		return service.QualityScopeConflict("restore this rule's isolated models and groups before deleting it")
	}
	if hasQualityCooldownOwnership(&state) {
		return service.QualityScopeConflict("restore this rule's cooled models and concurrency before deleting it")
	}
	// Revalidate live ownership as well as durable snapshots. A stale/cleared
	// snapshot must not let deletion leave a marker pointing at a missing rule.
	var liveIsolation, liveCooldowns []byte
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(extra->'quality_model_isolation','{}'::jsonb),COALESCE(extra->'model_rate_limits','{}'::jsonb) FROM accounts WHERE id=(SELECT account_id FROM scheduled_test_plans WHERE id=$1) FOR UPDATE`, id).Scan(&liveIsolation, &liveCooldowns)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	for _, live := range [][]byte{liveIsolation, liveCooldowns} {
		if len(live) == 0 {
			continue
		}
		var entries map[string]json.RawMessage
		if json.Unmarshal(live, &entries) != nil {
			return service.QualityScopeConflict("review unreadable model ownership before deleting this rule")
		}
		for _, raw := range entries {
			owner, _ := qualityEntryOwner(raw, id)
			if owner == "this_rule" {
				return service.QualityScopeConflict("review remaining model ownership before deleting this rule")
			}
		}
	}

	if _, err = tx.ExecContext(ctx, `DELETE FROM scheduled_test_plans WHERE id=$1`, id); err != nil {
		return err
	}
	return nil
}

func (r *scheduledTestPlanRepository) UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE scheduled_test_plans SET last_run_at = $2, next_run_at = $3, updated_at = NOW() WHERE id = $1
	`, id, lastRunAt, nextRunAt)
	return err
}

// --- Result Repository ---

type scheduledTestResultRepository struct {
	db *sql.DB
}

func NewScheduledTestResultRepository(db *sql.DB) service.ScheduledTestResultRepository {
	return &scheduledTestResultRepository{db: db}
}

func (r *scheduledTestResultRepository) Create(ctx context.Context, result *service.ScheduledTestResult) (*service.ScheduledTestResult, error) {
	return insertScheduledTestResult(ctx, r.db, result)
}

func insertScheduledTestResult(ctx context.Context, q planRowQuerier, result *service.ScheduledTestResult) (*service.ScheduledTestResult, error) {
	row := q.QueryRowContext(ctx, `
		INSERT INTO scheduled_test_results (plan_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at, pelican_config, quality_action, quality_judgment, quality_round_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), $8, $9, $10, $11)
		RETURNING id, plan_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at, pelican_config, quality_action, quality_judgment, quality_round_id
	`, result.PlanID, result.Status, result.ResponseText, result.ErrorMessage, result.LatencyMs, result.StartedAt, result.FinishedAt, marshalPelicanConfig(result.PelicanConfig), result.QualityAction, marshalQualityJudgment(result.QualityJudgment), result.QualityRoundID)

	out := &service.ScheduledTestResult{}
	var judgment []byte
	var config []byte
	if err := row.Scan(
		&out.ID, &out.PlanID, &out.Status, &out.ResponseText, &out.ErrorMessage,
		&out.LatencyMs, &out.StartedAt, &out.FinishedAt, &out.CreatedAt, &config, &out.QualityAction, &judgment, &out.QualityRoundID,
	); err != nil {
		return nil, err
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &out.PelicanConfig); err != nil {
			return nil, err
		}
	}
	if len(judgment) > 0 {
		if err := json.Unmarshal(judgment, &out.QualityJudgment); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *scheduledTestResultRepository) ListByPlanID(ctx context.Context, planID int64, limit int, includeContent ...bool) ([]*service.ScheduledTestResult, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, plan_id, status, CASE WHEN $3 THEN response_text ELSE '' END, error_message, latency_ms, started_at, finished_at, created_at, pelican_config, quality_action, quality_judgment, quality_round_id
		FROM scheduled_test_results
		WHERE plan_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2
	`, planID, limit, len(includeContent) == 0 || includeContent[0])
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []*service.ScheduledTestResult
	for rows.Next() {
		r := &service.ScheduledTestResult{}
		var judgment []byte
		var config []byte
		if err := rows.Scan(
			&r.ID, &r.PlanID, &r.Status, &r.ResponseText, &r.ErrorMessage,
			&r.LatencyMs, &r.StartedAt, &r.FinishedAt, &r.CreatedAt, &config, &r.QualityAction, &judgment, &r.QualityRoundID,
		); err != nil {
			return nil, err
		}
		if len(config) > 0 {
			if err := json.Unmarshal(config, &r.PelicanConfig); err != nil {
				return nil, err
			}
		}
		if len(judgment) > 0 {
			if err := json.Unmarshal(judgment, &r.QualityJudgment); err != nil {
				return nil, err
			}
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (r *scheduledTestResultRepository) PruneOldResults(ctx context.Context, planID int64, keepCount int) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM scheduled_test_results
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY plan_id ORDER BY created_at DESC, id DESC) AS rn
				FROM scheduled_test_results
				WHERE plan_id = $1
			) ranked
			WHERE rn > $2
		)
	`, planID, keepCount)
	return err
}

// --- scan helpers ---

type scannable interface {
	Scan(dest ...any) error
}

func scanPlan(row scannable, withAccountName ...bool) (*service.ScheduledTestPlan, error) {
	p := &service.ScheduledTestPlan{}
	var config []byte
	dest := []any{
		&p.ID, &p.AccountID, &p.ModelID, &p.CronExpression, &p.Enabled, &p.MaxResults, &p.AutoRecover,
		&p.LastRunAt, &p.NextRunAt, &p.CreatedAt, &p.UpdatedAt, &config, &p.RunningUntil,
	}
	if len(withAccountName) > 0 && withAccountName[0] {
		dest = append(dest, &p.AccountName)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &p.PelicanConfig); err != nil {
			return nil, err
		}
		var internal struct {
			ManualQualityRun bool `json:"_manual_quality_run"`
		}
		if err := json.Unmarshal(config, &internal); err != nil {
			return nil, err
		}
		p.ManualQualityRun = internal.ManualQualityRun
	}
	return p, nil
}

func scanPlans(rows *sql.Rows, withAccountName ...bool) ([]*service.ScheduledTestPlan, error) {
	var plans []*service.ScheduledTestPlan
	for rows.Next() {
		p, err := scanPlan(rows, withAccountName...)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func marshalPelicanConfig(config *service.PelicanTestConfig) any {
	if config == nil {
		return nil
	}
	data, _ := json.Marshal(config)
	return string(data)
}

// Compare the saved version as well as the due time: a concurrent pause/edit wins.
func (r *scheduledTestPlanRepository) ClaimPelican(ctx context.Context, plan *service.ScheduledTestPlan, now, until, next time.Time) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('pelican-account:' || $1::text, 0))`, plan.AccountID).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_test_plans
 SET running_until = $3, next_run_at = $4, pelican_config = pelican_config - '_manual_quality_run'
 WHERE id = $1 AND enabled = true
 AND (next_run_at <= $2 OR ($7 AND pelican_config->'quality'->>'trigger_on_upstream_5xx'='true'))
 AND (NOT $7 OR (pelican_config->'quality'->>'trigger_on_upstream_5xx'='true'
   AND EXISTS (SELECT 1 FROM accounts WHERE accounts.id=account_id AND type='oauth')
   AND ($8::timestamptz IS NULL OR last_run_at IS NULL OR last_run_at < $8 OR pelican_config->'quality'->>'action'='remove_models')))
 AND (running_until IS NULL OR running_until < $2) AND updated_at = $5 AND next_run_at = $6
 AND NOT EXISTS (SELECT 1 FROM account_quality_states rest WHERE rest.plan_id=scheduled_test_plans.id
 AND COALESCE((pelican_config->'quality'->>'retest_rest_minutes')::int,0)>0
 AND (rest.state->>'retest_after')::timestamptz>$2)
 AND EXISTS (SELECT 1 FROM accounts WHERE accounts.id = account_id AND deleted_at IS NULL)
 AND NOT EXISTS (SELECT 1 FROM scheduled_test_plans other WHERE other.account_id = scheduled_test_plans.account_id
 AND other.id <> scheduled_test_plans.id AND other.pelican_config IS NOT NULL AND other.running_until > $2)`, plan.ID, now, until, next, plan.UpdatedAt, plan.NextRunAt, plan.TriggerSource == "upstream_5xx", plan.TriggerObservedAt)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	var bpsRecoveryPending bool
	if n == 1 && plan.PelicanConfig != nil && plan.PelicanConfig.Quality != nil {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM account_quality_states WHERE plan_id=$1 AND state->>'action'=$2)`,
			plan.ID, service.QualityActionEnableBPS).Scan(&bpsRecoveryPending); err != nil {
			return false, err
		}
	}
	if n == 1 && plan.PelicanConfig != nil && plan.PelicanConfig.Quality != nil && plan.PelicanConfig.Quality.Action == service.QualityActionRemoveModel {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT (state->>'quality_5xx_episode')::bigint FROM account_quality_states WHERE plan_id=$1),0)`, plan.ID).Scan(&plan.Quality5xxEpisode); err != nil {
			return false, err
		}
	}
	if n == 1 && plan.PelicanConfig != nil && plan.PelicanConfig.Quality != nil && plan.PelicanConfig.Quality.OAuth401MaxRounds > 0 {
		if err := claimQualityOAuth401Tx(ctx, tx, plan, until); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if n == 1 && plan.PelicanConfig != nil {
		config := *plan.PelicanConfig
		config.BPSRecoveryPending = bpsRecoveryPending
		plan.PelicanConfig = &config
	}
	return n == 1, nil
}

func (r *scheduledTestPlanRepository) FinishPelican(ctx context.Context, id int64, until, finished time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE scheduled_test_plans SET running_until = NULL, last_run_at = $3,
 next_run_at=CASE WHEN pelican_config->'quality'->>'action'='openai_two_fa_admission'
 THEN $3::timestamptz+make_interval(secs => (pelican_config->'quality'->'admission'->>'wait_seconds')::int) ELSE next_run_at END
 WHERE id = $1 AND running_until = $2`, id, until, finished)
	return err
}

func (r *scheduledTestPlanRepository) FinishTriggeredQuality(ctx context.Context, plan *service.ScheduledTestPlan, until, finished, next time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE scheduled_test_plans SET running_until=NULL,last_run_at=$3,
 next_run_at=CASE WHEN updated_at=$4 AND next_run_at=$5 AND next_run_at<=$3 THEN $6 ELSE next_run_at END
 WHERE id=$1 AND running_until=$2`, plan.ID, until, finished, plan.UpdatedAt, plan.NextRunAt, next)
	return err
}

// Also prunes paused plans; bounded batches avoid long transactions on large histories.
func (r *scheduledTestResultRepository) PruneExpiredPelican(ctx context.Context, before time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM scheduled_test_results WHERE id IN (
 SELECT results.id FROM scheduled_test_results results
 JOIN scheduled_test_plans plans ON plans.id = results.plan_id
 WHERE plans.pelican_config IS NOT NULL AND results.created_at < $1
 ORDER BY results.created_at LIMIT 1000)`, before)
	return err
}

func (r *scheduledTestResultRepository) GetResult(ctx context.Context, planID, resultID int64) (*service.ScheduledTestResult, error) {
	out := &service.ScheduledTestResult{}
	var judgment []byte
	var config []byte
	err := r.db.QueryRowContext(ctx, `SELECT id, plan_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at, pelican_config, quality_action, quality_judgment, quality_round_id
 FROM scheduled_test_results WHERE plan_id = $1 AND id = $2`, planID, resultID).Scan(
		&out.ID, &out.PlanID, &out.Status, &out.ResponseText, &out.ErrorMessage,
		&out.LatencyMs, &out.StartedAt, &out.FinishedAt, &out.CreatedAt, &config, &out.QualityAction, &judgment, &out.QualityRoundID)
	if err != nil {
		return nil, err
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &out.PelicanConfig); err != nil {
			return nil, err
		}
	}
	if len(judgment) > 0 {
		if err := json.Unmarshal(judgment, &out.QualityJudgment); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListPelicanHistory lists every retained output, including failures and paused plans.
// Cursor pagination is independent of account-table filters or pages. Bodies are lazy-loaded.
// A non-zero accountID narrows the same history to that account's plans.
func (r *scheduledTestResultRepository) ListPelicanHistory(ctx context.Context, beforeID int64, limit int, accountID int64) ([]*service.PelicanHistoryResult, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT r.id, r.plan_id, r.status, r.error_message,
 r.latency_ms, r.started_at, r.finished_at, r.created_at, r.pelican_config, a.id, a.name
 FROM scheduled_test_results r
 JOIN scheduled_test_plans p ON p.id = r.plan_id
 JOIN accounts a ON a.id = p.account_id
 WHERE p.pelican_config IS NOT NULL AND p.pelican_config->'quality' IS NULL AND a.deleted_at IS NULL
 AND ($1::bigint = 0 OR r.id < $1)
 AND ($3::bigint = 0 OR p.account_id = $3)
 ORDER BY r.id DESC LIMIT $2`, beforeID, limit, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	results := make([]*service.PelicanHistoryResult, 0)
	for rows.Next() {
		result := &service.PelicanHistoryResult{}
		var config []byte
		if err := rows.Scan(&result.ID, &result.PlanID, &result.Status, &result.ErrorMessage,
			&result.LatencyMs, &result.StartedAt, &result.FinishedAt, &result.CreatedAt,
			&config, &result.AccountID, &result.AccountName); err != nil {
			return nil, err
		}
		if len(config) > 0 {
			if err := json.Unmarshal(config, &result.PelicanConfig); err != nil {
				return nil, err
			}
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

func marshalQualityJudgment(judgment *service.QualityJudgment) any {
	if judgment == nil {
		return nil
	}
	data, _ := json.Marshal(judgment)
	return string(data)
}
