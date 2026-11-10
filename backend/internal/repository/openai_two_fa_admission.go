package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// The caller holds the plan lease lock. Locking the account serializes these
// transitions with customer-driven concurrency upgrades and ordinary edits.
func applyOpenAITwoFAAdmissionOutcome(ctx context.Context, tx *sql.Tx, plan *service.ScheduledTestPlan, until time.Time, outcome string) (string, error) {
	var raw, extra []byte
	var concurrency int
	var status, platform, kind string
	var schedulable bool
	var parent sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(extra->'openai_two_fa_admission','null'::jsonb),status,platform,type,parent_account_id,schedulable,concurrency,COALESCE(extra,'{}'::jsonb)
 FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, plan.AccountID).Scan(&raw, &status, &platform, &kind, &parent, &schedulable, &concurrency, &extra)
	if err == sql.ErrNoRows {
		return "account_deleted", nil
	}
	if err != nil {
		return "", err
	}
	account := service.Account{Concurrency: concurrency}
	if err := json.Unmarshal(extra, &account.Extra); err != nil {
		return "", err
	}
	var state service.OpenAITwoFAAdmissionState
	if err := json.Unmarshal(raw, &state); err != nil {
		return "", err
	}
	if platform != service.PlatformOpenAI || kind != service.AccountTypeOAuth || parent.Valid || state.PlanID != plan.ID ||
		plan.PelicanConfig.Quality.Admission == nil || !reflect.DeepEqual(state.Policy, *plan.PelicanConfig.Quality.Admission) {
		return "admission_conflict", nil
	}
	// A paused/error account must not gain customer scheduling automatically.
	// Keep testing, and require a fresh sequence once the account is active.
	if status != service.StatusActive && outcome == "passed" {
		outcome = "inconclusive"
	}
	started := until.Add(-15 * time.Minute)
	// Use the unique lease as the durable round identity. Re-applying an action
	// after a lost response cannot count the same completed test twice.
	state, action := service.AdvanceOpenAITwoFAAdmission(state, outcome, started, time.Now().UTC())
	if action == "admission_isolated" {
		var valid bool
		groupErr := tx.QueryRowContext(ctx, `SELECT platform='openai' AND status='active' AND deleted_at IS NULL FROM groups WHERE id=$1 FOR SHARE`, state.Policy.IsolationGroupID).Scan(&valid)
		if groupErr != nil && groupErr != sql.ErrNoRows {
			return "", groupErr
		}
		if !valid || len(account.FilterOAuthDegradationGroups([]int64{state.Policy.IsolationGroupID})) == 0 {
			// Preserve the decision even if the target was disabled/deleted after
			// enrollment. Keep customer traffic off and retry on later tests;
			// a later answer of 21 cannot cancel this isolation decision.
			if state.Phase != "isolation_pending" {
				state.IsolationSchedulable = &schedulable
			}
			state.Phase, action = "isolation_pending", "admission_isolation_pending"
		} else {
			state.Phase = "isolated"
			before, err := qualityGroups(ctx, tx, plan.AccountID)
			if err != nil {
				return "", err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1`, plan.AccountID); err != nil {
				return "", err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority,created_at) VALUES($1,$2,50,NOW())`, plan.AccountID, state.Policy.IsolationGroupID); err != nil {
				return "", err
			}
			if err := qualityGroupsChanged(ctx, tx, plan.AccountID, before, []qualityGroup{{GroupID: state.Policy.IsolationGroupID}}); err != nil {
				return "", err
			}
		}
	}
	passedConcurrency := account.LimitOAuthDegradationConcurrency(state.Policy.PassedConcurrency)
	passedSchedulable := true
	if degradation, ok := account.Extra[service.OAuthDegradationActionExtraKey].(map[string]any); ok {
		nativeControls, _ := degradation["native_controls"].(bool)
		nativeSchedulingPaused, _ := degradation["native_scheduling_paused"].(bool)
		if nativeControls && nativeSchedulingPaused {
			// Admission must not undo a native pause imposed because the model
			// scope could not be represented. Keep an administrator's explicit
			// true as well: the audit marker is not a separate scheduling gate.
			passedSchedulable = schedulable
		}
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET
 extra=jsonb_set(CASE WHEN ($3 OR $5 OR $6) AND COALESCE((extra->'auto_config_concurrency'->>'degradation_hold')::boolean,false) IS NOT TRUE THEN COALESCE(extra,'{}'::jsonb)-'auto_config_concurrency' ELSE COALESCE(extra,'{}'::jsonb) END,'{openai_two_fa_admission}',$2::jsonb),
 concurrency=CASE WHEN $3 THEN $4 ELSE concurrency END,
 schedulable=CASE WHEN $3 THEN $8 WHEN $6 THEN false WHEN $5 AND $7::boolean IS NOT NULL THEN $7 ELSE schedulable END,
 updated_at=CASE WHEN $3 OR $5 OR $6 THEN clock_timestamp() ELSE updated_at END
 WHERE id=$1`, plan.AccountID, string(encoded), action == "admission_passed", passedConcurrency, action == "admission_isolated", action == "admission_isolation_pending", state.IsolationSchedulable, passedSchedulable)
	if err != nil {
		return "", err
	}
	if action == "admission_passed" || action == "admission_isolated" || action == "admission_isolation_pending" {
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &plan.AccountID, nil, nil); err != nil {
			return "", err
		}
	}
	return action, nil
}
