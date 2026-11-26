package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Keep only the two round IDs needed for backfill, independently of history retention.
type quality429State struct {
	Seq      int64          `json:"seq"`
	Account  int64          `json:"account"`
	Revision string         `json:"revision"`
	Rounds   []string       `json:"rounds"`
	Models   map[string]int `json:"models"`
}

// The claim sequence also advances for canceled, skipped and abandoned rounds.
// A completion with an expired lease cannot fill a gap behind the current owner.
func markQuality429Tx(ctx context.Context, tx *sql.Tx, plan *service.ScheduledTestPlan, until time.Time, results []*service.ScheduledTestResult, outcome string) (string, bool, error) {
	models := service.Quality429Models(plan, results)
	if len(models) == 0 {
		return outcome, false, nil
	}
	var raw []byte
	var seq int64
	var revision string
	err := tx.QueryRowContext(ctx, `SELECT quality_round_seq, quality_429_state, md5(jsonb_build_array(model_id,pelican_config)::text)
 FROM scheduled_test_plans WHERE id=$1 AND account_id=$2 AND enabled AND updated_at=$3
 AND running_until=$4 AND running_until>clock_timestamp() FOR UPDATE`, plan.ID, plan.AccountID, plan.UpdatedAt, until).Scan(&seq, &raw, &revision)
	if err == sql.ErrNoRows {
		return outcome, false, nil
	}
	if err != nil {
		return "", false, err
	}
	var prev quality429State
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &prev); err != nil {
			return "", false, err
		}
		if prev.Seq == seq {
			return outcome, true, nil
		}
	}
	if prev.Seq+1 != seq || prev.Account != plan.AccountID || prev.Revision != revision {
		prev = quality429State{}
	}
	next := quality429State{Seq: seq, Account: plan.AccountID, Revision: revision, Models: map[string]int{}}
	for _, model := range models {
		n := min(3, prev.Models[model]+1)
		next.Models[model] = n
		allowAction := true
		for _, result := range results {
			cfg := result.PelicanConfig
			if cfg.ModelID != model {
				continue
			}
			cfg.Quality429Streak = n
			if cfg.QuestionKind == service.QualityRandomQuestionKind && (cfg.QualityBatch == nil || !plan.QualityBatchEvaluated || cfg.QualityBatch.PeerPassed == 0 || cfg.QualityBatch.Decision != "inconclusive") {
				allowAction = false
			}
		}
		if n != 3 {
			continue
		}
		if prev.Models[model] == 2 {
			_, err = tx.ExecContext(ctx, `UPDATE scheduled_test_results SET pelican_config=jsonb_set(pelican_config,'{quality_429_streak}','3')
 WHERE plan_id=$1 AND quality_round_id=ANY($2) AND pelican_config->>'model_id'=$3
 AND pelican_config->>'quality_http_status'='429' AND pelican_config->>'quality_429_streak' IN ('1','2')`, plan.ID, pq.Array(prev.Rounds), model)
			if err != nil {
				return "", false, err
			}
		}
		// Admission and switching upstream protocols require independent evidence.
		q := plan.PelicanConfig.Quality
		if !allowAction || q.Action == service.QualityActionOpenAITwoFAAdmission || q.Action == service.QualityActionEnableBPS {
			continue
		}
		outcome = "failed"
		if _, ok := plan.QualityModelOutcomes[model]; ok {
			plan.QualityModelOutcomes[model] = "failed"
		}
		if q.Action == service.QualityActionRemoveModel && plan.PelicanConfig.QuestionKind == service.OpenAICodexStateProbeQuestionKind {
			for target := range plan.QualityModelOutcomes {
				plan.QualityModelOutcomes[target] = "failed"
			}
		}
	}
	next.Rounds = append(prev.Rounds, until.Format(time.RFC3339Nano))
	if len(next.Rounds) > 2 {
		next.Rounds = next.Rounds[len(next.Rounds)-2:]
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return "", false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE scheduled_test_plans SET quality_429_state=$2 WHERE id=$1`, plan.ID, string(encoded))
	return outcome, false, err
}

func (r *scheduledTestPlanRepository) SaveQuality429Round(ctx context.Context, plan *service.ScheduledTestPlan, until time.Time, results []*service.ScheduledTestResult, outcome string) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "SAVEPOINT quality_429_round"); err != nil {
		return "", err
	}
	outcome, duplicate, err := markQuality429Tx(ctx, tx, plan, until, results, outcome)
	if duplicate || err != nil {
		return "stale_run", err
	}
	if _, err = tx.ExecContext(ctx, "SAVEPOINT quality_429_action"); err != nil {
		return "", err
	}
	var commit bool
	action, err := applyQualityOutcomeTx(ctx, tx, plan, until, outcome, &commit)
	if err != nil {
		return "", err
	}
	if !commit || qualityActionRollsBack(action) {
		if _, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT quality_429_action"); err != nil {
			return "", err
		}
	}
	if action == "stale_run" || action == "account_deleted" {
		if _, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT quality_429_round"); err != nil {
			return "", err
		}
		// Serialize a late delivery with both current and other stale writers.
		var alreadySaved bool
		if err = tx.QueryRowContext(ctx, `SELECT id FROM scheduled_test_plans WHERE id=$1 FOR UPDATE`, plan.ID).Scan(new(int64)); err == sql.ErrNoRows {
			return action, nil
		} else if err != nil {
			return "", err
		}
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM scheduled_test_results WHERE plan_id=$1 AND quality_round_id=$2)`, plan.ID, until.Format(time.RFC3339Nano)).Scan(&alreadySaved); err != nil || alreadySaved {
			return action, err
		}
		for _, result := range results {
			result.PelicanConfig.Quality429Streak = 0
		}
	}
	for _, result := range results {
		result.PlanID, result.QualityRoundID, result.QualityAction = plan.ID, until.Format(time.RFC3339Nano), action
		cfg := result.PelicanConfig
		cfg.QualityModelOutcomes = plan.QualityModelOutcomes
		if commit && !qualityActionRollsBack(action) {
			cfg.QualityModelActions = plan.QualityModelActions
		}
		if action == "restore_conflict" {
			cfg.QualityRestoreDiagnostic = plan.QualityRestoreDiagnostic
		}
		if _, err = insertScheduledTestResult(ctx, tx, result); err != nil {
			return "", err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM scheduled_test_results WHERE plan_id=$1 AND id NOT IN
 (SELECT id FROM scheduled_test_results WHERE plan_id=$1 ORDER BY id DESC LIMIT $2)`, plan.ID, max(1, plan.MaxResults)); err != nil {
		return "", err
	}
	return action, tx.Commit()
}
