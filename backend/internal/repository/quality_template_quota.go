package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *qualityRuleTemplateRepository) ForPlan(ctx context.Context, planID int64) (*service.QualityRuleTemplate, error) {
	var filter []byte
	err := r.db.QueryRowContext(ctx, `SELECT t.account_filter FROM quality_rule_templates t
 JOIN quality_rule_template_accounts link ON link.template_id=t.id WHERE link.plan_id=$1`, planID).Scan(&filter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t := &service.QualityRuleTemplate{}
	if err := json.Unmarshal(filter, &t.AccountFilter); err != nil {
		return nil, err
	}
	return t, nil
}

var _ service.QualityQuotaSkipRepository = (*scheduledTestPlanRepository)(nil)

func (r *scheduledTestPlanRepository) FinishQuotaSkippedPelican(ctx context.Context, plan *service.ScheduledTestPlan, until, finished time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var current bool
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(running_until=$2,false) FROM scheduled_test_plans WHERE id=$1 FOR UPDATE`, plan.ID, until).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || !current {
		return err
	}
	// Undo only this unexecuted ledger claim. Keep the completed round and its
	// failures/revocation evidence; no 401 was observed during a quota skip.
	if _, err = tx.ExecContext(ctx, `UPDATE quality_oauth_401_streaks
 SET claimed_until=COALESCE(last_round_until,claimed_until) WHERE plan_id=$1 AND claimed_until=$2`, plan.ID, until); err != nil {
		return err
	}
	// No probe ran: last_run_at must still describe the last executed round.
	// Advancing it here would discard an earlier queued upstream 5xx signal.
	if _, err = tx.ExecContext(ctx, `UPDATE scheduled_test_plans SET running_until=NULL
 WHERE id=$1 AND running_until=$2`, plan.ID, until); err != nil {
		return err
	}
	return tx.Commit()
}
