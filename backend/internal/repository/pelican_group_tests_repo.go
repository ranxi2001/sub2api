package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type pelicanGroupTestRepository struct {
	configuredTimezone
	db *sql.DB
}

func NewPelicanGroupTestRepository(db *sql.DB) service.PelicanGroupTestRepository {
	return &pelicanGroupTestRepository{db: db}
}

// Plans read with their group and newest result. A soft-deleted group reports status
// "deleted" so the admin page can offer to remove its plans.
const pelicanGroupTestPlanSelect = `SELECT p.id, p.group_id, g.name, g.platform,
 CASE WHEN g.deleted_at IS NULL THEN g.status ELSE 'deleted' END,
 p.model_id, p.cron_expression, p.enabled, p.pelican_config, p.last_run_at, p.next_run_at, p.running_until, p.created_at, p.updated_at,
 COALESCE(c.today_cost, 0), COALESCE(c.total_cost, 0), COALESCE(c.today_incomplete, false), COALESCE(c.total_incomplete, false),
 r.id, r.account_id, r.account_name, r.attempts, r.status, r.error_message, r.latency_ms, r.started_at, r.finished_at, r.created_at, r.cost_usd, r.cost_incomplete
FROM pelican_group_test_plans p
JOIN groups g ON g.id = p.group_id
LEFT JOIN LATERAL (
 SELECT SUM(cost_usd) FILTER (WHERE cost_date = $1::date) AS today_cost, SUM(cost_usd) AS total_cost,
 BOOL_OR(unpriced_count > 0) FILTER (WHERE cost_date = $1::date) AS today_incomplete,
 BOOL_OR(unpriced_count > 0) AS total_incomplete
 FROM pelican_group_test_daily_costs WHERE group_id = p.group_id
) c ON true
LEFT JOIN LATERAL (
 SELECT id, account_id, account_name, attempts, status, error_message, latency_ms, started_at, finished_at, created_at, cost_usd, cost_incomplete
 FROM pelican_group_test_results WHERE plan_id = p.id ORDER BY id DESC LIMIT 1
) r ON true`

func (r *pelicanGroupTestRepository) ListPlans(ctx context.Context) ([]*service.PelicanGroupTestPlan, error) {
	rows, err := r.db.QueryContext(ctx, pelicanGroupTestPlanSelect+` ORDER BY g.sort_order ASC, g.id ASC, p.id ASC`, r.groupUsageDate(time.Now()))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	plans := make([]*service.PelicanGroupTestPlan, 0)
	for rows.Next() {
		plan, err := scanPelicanGroupTestPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func (r *pelicanGroupTestRepository) GetPlan(ctx context.Context, id int64) (*service.PelicanGroupTestPlan, error) {
	plan, err := scanPelicanGroupTestPlan(r.db.QueryRowContext(ctx, pelicanGroupTestPlanSelect+` WHERE p.id = $2`, r.groupUsageDate(time.Now()), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return plan, err
}

func (r *pelicanGroupTestRepository) CreatePlan(ctx context.Context, plan *service.PelicanGroupTestPlan) (*service.PelicanGroupTestPlan, error) {
	var id int64
	if err := r.db.QueryRowContext(ctx, `INSERT INTO pelican_group_test_plans
 (group_id, model_id, cron_expression, enabled, pelican_config, next_run_at)
 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		plan.GroupID, plan.ModelID, plan.CronExpression, plan.Enabled, marshalPelicanConfig(plan.PelicanConfig), plan.NextRunAt).Scan(&id); err != nil {
		return nil, err
	}
	return r.GetPlan(ctx, id)
}

func (r *pelicanGroupTestRepository) UpdatePlan(ctx context.Context, plan *service.PelicanGroupTestPlan) (*service.PelicanGroupTestPlan, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE pelican_group_test_plans
 SET model_id = $2, cron_expression = $3, enabled = $4, pelican_config = $5, next_run_at = $6, updated_at = NOW()
 WHERE id = $1`,
		plan.ID, plan.ModelID, plan.CronExpression, plan.Enabled, marshalPelicanConfig(plan.PelicanConfig), plan.NextRunAt)
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		return nil, err
	}
	return r.GetPlan(ctx, plan.ID)
}

func (r *pelicanGroupTestRepository) DeletePlan(ctx context.Context, id int64) (bool, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM pelican_group_test_plans WHERE id = $1`, id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func (r *pelicanGroupTestRepository) ListDue(ctx context.Context, now time.Time) ([]*service.PelicanGroupTestPlan, error) {
	rows, err := r.db.QueryContext(ctx, pelicanGroupTestPlanSelect+`
 WHERE p.enabled = true AND p.next_run_at <= $2 AND (p.running_until IS NULL OR p.running_until < $2)
 AND g.deleted_at IS NULL AND g.status = $3
 ORDER BY p.next_run_at ASC, p.id ASC`, r.groupUsageDate(now), now, service.StatusActive)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	plans := make([]*service.PelicanGroupTestPlan, 0)
	for rows.Next() {
		plan, err := scanPelicanGroupTestPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func (r *pelicanGroupTestRepository) Claim(ctx context.Context, plan *service.PelicanGroupTestPlan, now, until time.Time, next *time.Time) (bool, error) {
	var result sql.Result
	var err error
	if next == nil {
		result, err = r.db.ExecContext(ctx, `UPDATE pelican_group_test_plans SET running_until = $3
 WHERE id = $1 AND (running_until IS NULL OR running_until < $2)`, plan.ID, now, until)
	} else {
		// Claim/Finish leave updated_at unchanged. Bind the listed nominal
		// round too, so a delayed snapshot cannot consume a later due round.
		result, err = r.db.ExecContext(ctx, `UPDATE pelican_group_test_plans SET running_until = $3, next_run_at = $4
 WHERE id = $1 AND enabled = true AND next_run_at <= $2 AND (running_until IS NULL OR running_until < $2) AND updated_at = $5 AND next_run_at = $6`,
			plan.ID, now, until, *next, plan.UpdatedAt, plan.NextRunAt)
	}
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (r *pelicanGroupTestRepository) Finish(ctx context.Context, id int64, until, finished time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE pelican_group_test_plans SET running_until = NULL, last_run_at = $3
 WHERE id = $1 AND running_until = $2`, id, until, finished)
	return err
}

func (r *pelicanGroupTestRepository) CreateResult(ctx context.Context, result *service.PelicanGroupTestResult) (*service.PelicanGroupTestResult, error) {
	attempts, err := json.Marshal(nonNilAttempts(result.Attempts))
	if err != nil {
		return nil, err
	}
	saved := *result
	var accountID any
	if result.AccountID > 0 {
		accountID = result.AccountID
	}
	// The result and its retained daily aggregate commit atomically, including failed
	// generations. The group comes from the persisted plan, not caller input.
	if err := r.db.QueryRowContext(ctx, `WITH saved AS (
 INSERT INTO pelican_group_test_results
 (plan_id, account_id, account_name, attempts, status, response_text, error_message, latency_ms, pelican_config, started_at, finished_at, cost_usd, cost_incomplete)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
 RETURNING id, created_at, plan_id, cost_usd, cost_incomplete
), costs AS (
 INSERT INTO pelican_group_test_daily_costs (group_id, cost_date, cost_usd, unpriced_count)
 SELECT p.group_id, $14::date, COALESCE(s.cost_usd, 0),
 CASE WHEN s.cost_usd IS NULL OR s.cost_incomplete THEN 1 ELSE 0 END
 FROM saved s JOIN pelican_group_test_plans p ON p.id = s.plan_id
 ON CONFLICT (group_id, cost_date) DO UPDATE SET
 cost_usd = pelican_group_test_daily_costs.cost_usd + EXCLUDED.cost_usd,
 unpriced_count = pelican_group_test_daily_costs.unpriced_count + EXCLUDED.unpriced_count
) SELECT id, created_at FROM saved`,
		result.PlanID, accountID, result.AccountName, attempts, result.Status, result.ResponseText, result.ErrorMessage,
		result.LatencyMs, marshalPelicanConfig(result.PelicanConfig), result.StartedAt, result.FinishedAt,
		result.CostUSD, result.CostIncomplete, r.groupUsageDate(result.FinishedAt)).Scan(&saved.ID, &saved.CreatedAt); err != nil {
		return nil, err
	}
	return &saved, nil
}

func (r *pelicanGroupTestRepository) PruneResults(ctx context.Context, planID int64, keep int) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM pelican_group_test_results WHERE plan_id = $1 AND id NOT IN (
 SELECT id FROM pelican_group_test_results WHERE plan_id = $1 ORDER BY id DESC LIMIT $2)`, planID, keep)
	return err
}

// PruneExpiredResults runs every minute; bounded batches keep each transaction short.
func (r *pelicanGroupTestRepository) PruneExpiredResults(ctx context.Context, before time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM pelican_group_test_results WHERE id IN (
 SELECT id FROM pelican_group_test_results WHERE created_at < $1 LIMIT 1000)`, before)
	return err
}

const pelicanGroupTestResultSelect = `SELECT r.id, r.plan_id, p.group_id, g.name, COALESCE(r.account_id, 0), r.account_name, r.attempts,
 r.status, r.error_message, r.latency_ms, r.pelican_config, r.started_at, r.finished_at, r.created_at, r.cost_usd, r.cost_incomplete`

const pelicanGroupTestResultFrom = `
 FROM pelican_group_test_results r
 JOIN pelican_group_test_plans p ON p.id = r.plan_id
 JOIN groups g ON g.id = p.group_id
 WHERE ($1::bigint = 0 OR r.plan_id = $1)`

func (r *pelicanGroupTestRepository) ListResults(ctx context.Context, planID int64, offset, limit int) ([]*service.PelicanGroupTestResult, int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*)`+pelicanGroupTestResultFrom, planID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, pelicanGroupTestResultSelect+pelicanGroupTestResultFrom+`
 ORDER BY r.id DESC LIMIT $2 OFFSET $3`, planID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	results := make([]*service.PelicanGroupTestResult, 0, limit)
	for rows.Next() {
		result, err := scanPelicanGroupTestResult(rows)
		if err != nil {
			return nil, 0, err
		}
		results = append(results, result)
	}
	return results, total, rows.Err()
}

func (r *pelicanGroupTestRepository) GetResult(ctx context.Context, id int64) (*service.PelicanGroupTestResult, error) {
	row := r.db.QueryRowContext(ctx, pelicanGroupTestResultSelect+`, r.response_text
 FROM pelican_group_test_results r
 JOIN pelican_group_test_plans p ON p.id = r.plan_id
 JOIN groups g ON g.id = p.group_id
 WHERE r.id = $1`, id)
	result, err := scanPelicanGroupTestResult(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return result, err
}

func scanPelicanGroupTestPlan(row scannable) (*service.PelicanGroupTestPlan, error) {
	plan := &service.PelicanGroupTestPlan{}
	var config []byte
	var lastRun, nextRun, runningUntil sql.NullTime
	var resultID, resultAccountID, resultLatency sql.NullInt64
	var resultAccountName, resultStatus, resultError sql.NullString
	var resultAttempts []byte
	var resultStarted, resultFinished, resultCreated sql.NullTime
	var resultCost sql.NullFloat64
	var resultIncomplete sql.NullBool
	if err := row.Scan(&plan.ID, &plan.GroupID, &plan.GroupName, &plan.GroupPlatform, &plan.GroupStatus,
		&plan.ModelID, &plan.CronExpression, &plan.Enabled, &config, &lastRun, &nextRun, &runningUntil, &plan.CreatedAt, &plan.UpdatedAt,
		&plan.TodayCostUSD, &plan.TotalCostUSD, &plan.TodayCostIncomplete, &plan.TotalCostIncomplete,
		&resultID, &resultAccountID, &resultAccountName, &resultAttempts, &resultStatus, &resultError, &resultLatency,
		&resultStarted, &resultFinished, &resultCreated, &resultCost, &resultIncomplete); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(config, &plan.PelicanConfig); err != nil {
		return nil, err
	}
	plan.LastRunAt = nullTimePtr(lastRun)
	plan.NextRunAt = nullTimePtr(nextRun)
	plan.RunningUntil = nullTimePtr(runningUntil)
	if resultID.Valid {
		last := &service.PelicanGroupTestResult{
			ID: resultID.Int64, PlanID: plan.ID, GroupID: plan.GroupID, GroupName: plan.GroupName,
			AccountID: resultAccountID.Int64, AccountName: resultAccountName.String, Status: resultStatus.String,
			ErrorMessage: resultError.String, LatencyMs: resultLatency.Int64,
			StartedAt: resultStarted.Time, FinishedAt: resultFinished.Time, CreatedAt: resultCreated.Time,
			CostIncomplete: resultIncomplete.Bool,
		}
		if resultCost.Valid {
			last.CostUSD = &resultCost.Float64
		}
		if err := unmarshalAttempts(resultAttempts, &last.Attempts); err != nil {
			return nil, err
		}
		plan.LastResult = last
	}
	return plan, nil
}

func scanPelicanGroupTestResult(row scannable, withContent ...bool) (*service.PelicanGroupTestResult, error) {
	result := &service.PelicanGroupTestResult{}
	var attempts, config []byte
	dest := []any{&result.ID, &result.PlanID, &result.GroupID, &result.GroupName, &result.AccountID, &result.AccountName, &attempts,
		&result.Status, &result.ErrorMessage, &result.LatencyMs, &config, &result.StartedAt, &result.FinishedAt, &result.CreatedAt, &result.CostUSD, &result.CostIncomplete}
	if len(withContent) > 0 && withContent[0] {
		dest = append(dest, &result.ResponseText)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	if err := unmarshalAttempts(attempts, &result.Attempts); err != nil {
		return nil, err
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &result.PelicanConfig); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func unmarshalAttempts(raw []byte, attempts *[]service.PelicanGroupTestAttempt) error {
	*attempts = []service.PelicanGroupTestAttempt{}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, attempts)
}

func nonNilAttempts(attempts []service.PelicanGroupTestAttempt) []service.PelicanGroupTestAttempt {
	if attempts == nil {
		return []service.PelicanGroupTestAttempt{}
	}
	return attempts
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}
