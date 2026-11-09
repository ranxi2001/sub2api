package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Reflection lets this regression fail on the original implementation without
// requiring the new diagnostic type to exist first.
func restoreDiagnosticJSON(t *testing.T, plan *service.ScheduledTestPlan) map[string]any {
	t.Helper()
	field := reflect.ValueOf(plan).Elem().FieldByName("QualityRestoreDiagnostic")
	require.True(t, field.IsValid(), "restore conflicts must carry a structured reason")
	raw, err := json.Marshal(field.Interface())
	require.NoError(t, err)
	var diagnostic map[string]any
	require.NoError(t, json.Unmarshal(raw, &diagnostic))
	require.NotNil(t, diagnostic)
	return diagnostic
}

func TestQualityRestoreConflictRepeatedManualMembership(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
	}()
	now := time.Now().UTC().Truncate(time.Microsecond)
	plan := &service.ScheduledTestPlan{ID: 7, AccountID: 8, UpdatedAt: now,
		PelicanConfig: &service.PelicanTestConfig{Quality: &service.QualityPolicy{Action: "remove_groups", AutoRestore: true}}}
	state := `{"action":"remove_groups","account_version":"2026-01-01T00:00:00Z","removed":[{"account_id":8,"group_id":21,"priority":7}]}`
	for round := 0; round < 2; round++ {
		until := now.Add(time.Duration(round+1) * time.Minute)
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT enabled AND updated_at").WithArgs(plan.ID, plan.UpdatedAt, until).WillReturnRows(sqlmock.NewRows([]string{"valid"}).AddRow(true))
		mock.ExpectQuery("SELECT state FROM account_quality_states").WithArgs(plan.ID).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(state))
		mock.ExpectQuery("SELECT updated_at, schedulable, status FROM accounts").WithArgs(plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"updated_at", "schedulable", "status"}).AddRow(now, true, "active"))
		mock.ExpectQuery("SELECT COALESCE\\(jsonb_agg").WithArgs(plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"groups"}).AddRow(`[{"account_id":8,"group_id":21,"priority":50}]`))
		expectOAuthDegradationGroupBindingsAccount(mock, plan.AccountID, `{}`)
		mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM groups").WithArgs(int64(21), plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"exists", "replaced"}).AddRow(true, true))
		mock.ExpectRollback()
		action, err := NewScheduledTestPlanRepository(db).ApplyQualityOutcome(context.Background(), plan, until, "passed")
		require.NoError(t, err)
		require.Equal(t, "restore_conflict", action, "external membership must never be claimed as a rule restore")
		require.NoError(t, mock.ExpectationsWereMet(), "no account/group/state/outbox writes are allowed")
		diagnostic := restoreDiagnosticJSON(t, plan)
		require.Equal(t, "external_membership_present", diagnostic["disposition"])
		require.Equal(t, true, diagnostic["ownership_retained"])
		require.Contains(t, string(mustJSON(t, diagnostic)), "membership_already_present")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

func TestQualityRestoreDiagnosticsGroupEvidenceAndNoWrites(t *testing.T) {
	for _, tc := range []struct {
		name        string
		live, bound [2]bool
		reason      string
		disposition string
	}{
		{"all externally present", [2]bool{true, true}, [2]bool{true, true}, "membership_already_present", "external_membership_present"},
		{"partially present", [2]bool{true, true}, [2]bool{true, false}, "membership_already_present", "manual_required"},
		{"deleted group after present group", [2]bool{true, false}, [2]bool{true, false}, "group_missing", "manual_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() {
				mock.ExpectClose()
				require.NoError(t, db.Close())
			}()
			mock.ExpectBegin()
			tx, err := db.Begin()
			require.NoError(t, err)
			state := qualityState{Action: "remove_groups", Removed: []qualityGroup{{AccountID: 8, GroupID: 21, Priority: 7}, {AccountID: 8, GroupID: 22, Priority: 9}}}
			before := mustJSON(t, state)
			expectOAuthDegradationGroupBindingsAccount(mock, int64(8), `{}`)
			for i, group := range state.Removed {
				mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM groups").WithArgs(group.GroupID, int64(8)).WillReturnRows(sqlmock.NewRows([]string{"exists", "replaced"}).AddRow(tc.live[i], tc.bound[i]))
			}
			mock.ExpectRollback()
			conflict, err := restoreQualityGroups(context.Background(), tx, 8, &state)
			require.NoError(t, err)
			require.True(t, conflict)
			require.Equal(t, tc.reason, state.RestoreDiagnostic.Reason)
			require.Equal(t, tc.disposition, state.RestoreDiagnostic.Disposition)
			require.Len(t, state.RestoreDiagnostic.Groups, 2, "diagnose all targets before any mutation")
			require.JSONEq(t, string(before), string(mustJSON(t, state)), "ownership JSON must not change")
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func expectQualityDiagnosticAccount(mock sqlmock.Sqlmock, plan *service.ScheduledTestPlan, until time.Time, state string, status string) {
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT enabled AND updated_at").WithArgs(plan.ID, plan.UpdatedAt, until).WillReturnRows(sqlmock.NewRows([]string{"valid"}).AddRow(true))
	mock.ExpectQuery("SELECT state FROM account_quality_states").WithArgs(plan.ID).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(state))
	mock.ExpectQuery("SELECT updated_at, schedulable, status FROM accounts").WithArgs(plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"updated_at", "schedulable", "status"}).AddRow(plan.UpdatedAt, false, status))
}

func TestQualityRestoreDiagnosticsIgnoreUnrelatedAccountVersion(t *testing.T) {
	for _, action := range []string{"remove_groups", "disable_scheduling"} {
		t.Run(action, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() {
				mock.ExpectClose()
				require.NoError(t, db.Close())
			}()
			plan := &service.ScheduledTestPlan{ID: 7, AccountID: 8, UpdatedAt: time.Now(), PelicanConfig: &service.PelicanTestConfig{Quality: &service.QualityPolicy{Action: action, AutoRestore: true}}}
			until := plan.UpdatedAt.Add(time.Minute)
			state := qualityState{Action: action, AccountVersion: plan.UpdatedAt.Add(-time.Hour)}
			if action == "remove_groups" {
				state.Removed = []qualityGroup{{AccountID: 8, GroupID: 21, Priority: 7}}
			}
			expectQualityDiagnosticAccount(mock, plan, until, string(mustJSON(t, state)), "active")
			mock.ExpectQuery("SELECT COALESCE\\(jsonb_agg").WithArgs(plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"groups"}).AddRow(`[]`))
			if action == "remove_groups" {
				expectOAuthDegradationGroupBindingsAccount(mock, plan.AccountID, `{}`)
				mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM groups").WithArgs(int64(21), plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"exists", "replaced"}).AddRow(true, false))
				mock.ExpectExec("INSERT INTO account_groups").WithArgs(plan.AccountID, int64(21), 7, state.Removed[0].CreatedAt, nil).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UPDATE accounts SET updated_at").WithArgs(plan.AccountID).WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectExec("UPDATE accounts SET schedulable=true").WithArgs(plan.AccountID).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery("SELECT schedulable FROM accounts").WithArgs(plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"schedulable"}).AddRow(true))
			}
			mock.ExpectExec("DELETE FROM account_quality_states").WithArgs(plan.ID).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("INSERT INTO scheduler_outbox").WithArgs(plan.AccountID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			got, err := NewScheduledTestPlanRepository(db).ApplyQualityOutcome(context.Background(), plan, until, "passed")
			require.NoError(t, err)
			require.Equal(t, "restored", got)
			require.Nil(t, plan.QualityRestoreDiagnostic)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestQualityRestoreDiagnosticsAccountGuards(t *testing.T) {
	for _, status := range []string{"disabled", "active"} {
		t.Run(status, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() {
				mock.ExpectClose()
				require.NoError(t, db.Close())
			}()
			plan := &service.ScheduledTestPlan{ID: 7, AccountID: 8, UpdatedAt: time.Now(), PelicanConfig: &service.PelicanTestConfig{Quality: &service.QualityPolicy{Action: "disable_scheduling", AutoRestore: true}}}
			until := plan.UpdatedAt.Add(time.Minute)
			expectQualityDiagnosticAccount(mock, plan, until, `{"action":"disable_scheduling"}`, status)
			mock.ExpectQuery("SELECT COALESCE\\(jsonb_agg").WithArgs(plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"groups"}).AddRow(`[]`))
			reason := "account_not_active"
			if status == "active" {
				reason = "account_expired"
				mock.ExpectExec("UPDATE accounts SET schedulable=true").WithArgs(plan.AccountID).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectQuery("SELECT schedulable FROM accounts").WithArgs(plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"schedulable"}).AddRow(false))
			}
			mock.ExpectRollback()
			got, err := NewScheduledTestPlanRepository(db).ApplyQualityOutcome(context.Background(), plan, until, "passed")
			require.NoError(t, err)
			require.Equal(t, "restore_conflict", got)
			require.Equal(t, reason, plan.QualityRestoreDiagnostic.Reason)
			require.Equal(t, "manual_required", plan.QualityRestoreDiagnostic.Disposition)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestQualityRestoreDiagnosticsStaleRunClearsPreviousEvidence(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
	}()
	plan := &service.ScheduledTestPlan{ID: 7, UpdatedAt: time.Now(), QualityRestoreDiagnostic: qualityRestoreConflict("group_missing")}
	until := plan.UpdatedAt.Add(time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT enabled AND updated_at").WithArgs(plan.ID, plan.UpdatedAt, until).WillReturnRows(sqlmock.NewRows([]string{"valid"}).AddRow(false))
	mock.ExpectRollback()
	action, err := NewScheduledTestPlanRepository(db).ApplyQualityOutcome(context.Background(), plan, until, "passed")
	require.NoError(t, err)
	require.Equal(t, "stale_run", action)
	require.Nil(t, plan.QualityRestoreDiagnostic)
	require.NoError(t, mock.ExpectationsWereMet(), "stale runs cannot read ownership or accounts")
}

func TestQualityOutcomeMissingConfigStopsAfterLeaseValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *service.PelicanTestConfig
	}{
		{name: "missing-config"},
		{name: "missing-quality-policy", cfg: &service.PelicanTestConfig{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() {
				mock.ExpectClose()
				require.NoError(t, db.Close())
			}()
			plan := &service.ScheduledTestPlan{ID: 7, UpdatedAt: time.Now(), PelicanConfig: tc.cfg,
				QualityRestoreDiagnostic: qualityRestoreConflict("group_missing")}
			until := plan.UpdatedAt.Add(time.Minute)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT enabled AND updated_at").WithArgs(plan.ID, plan.UpdatedAt, until).
				WillReturnRows(sqlmock.NewRows([]string{"valid"}).AddRow(true))
			mock.ExpectRollback()
			action, err := NewScheduledTestPlanRepository(db).ApplyQualityOutcome(context.Background(), plan, until, "passed")
			require.NoError(t, err)
			require.Equal(t, "inconclusive", action)
			require.Nil(t, plan.QualityRestoreDiagnostic)
			require.NoError(t, mock.ExpectationsWereMet(), "missing configs cannot read ownership or accounts")
		})
	}
}

// Concurrent invocation exercises both guard paths; sqlmock cannot prove actual
// PostgreSQL row/advisory locking. Neither caller is permitted any write here.
func TestQualityRestoreDiagnosticsConcurrentRulesCannotTakeOwnership(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		// Concurrent transactions may open more than one driver connection.
		for remaining := db.Stats().OpenConnections; remaining > 0; remaining-- {
			mock.ExpectClose()
		}
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	}()
	mock.MatchExpectationsInOrder(false)
	now := time.Now()
	until := now.Add(time.Minute)
	plans := []*service.ScheduledTestPlan{}
	for _, id := range []int64{7, 9} {
		plan := &service.ScheduledTestPlan{ID: id, AccountID: 8, UpdatedAt: now, PelicanConfig: &service.PelicanTestConfig{Quality: &service.QualityPolicy{Action: "remove_groups", AutoRestore: true}}}
		plans = append(plans, plan)
		state := `{"action":"remove_groups","removed":[{"account_id":8,"group_id":21}]}`
		if id == 9 {
			state = ""
		}
		expectQualityDiagnosticAccount(mock, plan, until, state, "active")
		mock.ExpectRollback()
	}
	mock.ExpectQuery("SELECT EXISTS\\( SELECT 1 FROM account_quality_states").WithArgs(int64(8), int64(9), false).WillReturnRows(sqlmock.NewRows([]string{"owned"}).AddRow(true))
	mock.ExpectQuery("SELECT COALESCE\\(jsonb_agg").WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"groups"}).AddRow(`[]`))
	expectOAuthDegradationGroupBindingsAccount(mock, int64(8), `{}`)
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM groups").WithArgs(int64(21), int64(8)).WillReturnRows(sqlmock.NewRows([]string{"exists", "replaced"}).AddRow(true, true))
	var wg sync.WaitGroup
	actions, errs := make([]string, 2), make([]error, 2)
	for i, plan := range plans {
		wg.Add(1)
		go func(i int, plan *service.ScheduledTestPlan) {
			defer wg.Done()
			actions[i], errs[i] = NewScheduledTestPlanRepository(db).ApplyQualityOutcome(context.Background(), plan, until, "passed")
		}(i, plan)
	}
	wg.Wait()
	require.Equal(t, []error{nil, nil}, errs)
	require.Equal(t, []string{"restore_conflict", "action_conflict"}, actions)
	require.Nil(t, plans[1].QualityRestoreDiagnostic)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQualityRestoreDiagnosticsMissingAccountIsNotRestoreConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
	}()
	plan := &service.ScheduledTestPlan{ID: 7, AccountID: 8, UpdatedAt: time.Now(), PelicanConfig: &service.PelicanTestConfig{Quality: &service.QualityPolicy{Action: "remove_groups", AutoRestore: true}}}
	until := plan.UpdatedAt.Add(time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT enabled AND updated_at").WillReturnRows(sqlmock.NewRows([]string{"valid"}).AddRow(true))
	mock.ExpectQuery("SELECT state FROM account_quality_states").WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(`{"action":"remove_groups"}`))
	mock.ExpectQuery("SELECT updated_at, schedulable, status FROM accounts").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	action, err := NewScheduledTestPlanRepository(db).ApplyQualityOutcome(context.Background(), plan, until, "passed")
	require.NoError(t, err)
	require.Equal(t, "account_deleted", action)
	require.Nil(t, plan.QualityRestoreDiagnostic)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQualityRestoreDiagnosticsIsolationCannotClaimExternalGroups(t *testing.T) {
	for _, changedModel := range []bool{false, true} {
		t.Run(map[bool]string{false: "external group with model rollback", true: "foreign model entry"}[changedModel], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() {
				mock.ExpectClose()
				require.NoError(t, db.Close())
			}()
			now := time.Now()
			plan := &service.ScheduledTestPlan{ID: 7, AccountID: 8, ModelID: "sol", UpdatedAt: now,
				QualityModelOutcomes: map[string]string{"sol": "passed", "astra": "passed"},
				PelicanConfig:        &service.PelicanTestConfig{ModelIDs: []string{"sol", "astra"}, Quality: &service.QualityPolicy{Action: service.QualityActionIsolateModels, RemoveGroupIDs: []int64{21}, AutoRestore: true}}}
			account := &service.Account{Extra: map[string]any{}}
			models := &qualityState{PersistentModels: true}
			_, _, err = transitionQualityModelRound(account, models, plan.ID, []string{"sol", "astra"}, map[string]string{"sol": "failed", "astra": "failed"}, time.Time{}, true, now, true)
			require.NoError(t, err)
			state := qualityState{Action: service.QualityActionIsolateModels, Models: models,
				Groups:          &qualityState{Action: "remove_groups", Removed: []qualityGroup{{AccountID: 8, GroupID: 21}}},
				IsolationModels: []string{"sol", "astra"}, IsolationGroupIDs: []int64{21}}
			if changedModel {
				entries, ok := account.Extra[service.QualityModelIsolationKey].(map[string]any)
				require.True(t, ok)
				entries["sol"] = map[string]any{"reason": "external-policy"}
			}
			until := now.Add(time.Minute)
			expectQualityDiagnosticAccount(mock, plan, until, string(mustJSON(t, state)), "active")
			mock.ExpectQuery("SELECT credentials,COALESCE").WithArgs(plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"credentials", "extra"}).AddRow(`{}`, string(mustJSON(t, account.Extra))))
			mock.ExpectQuery("SELECT COALESCE\\(jsonb_agg").WithArgs(plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"groups"}).AddRow(`[{"account_id":8,"group_id":21}]`))
			if !changedModel {
				expectOAuthDegradationGroupBindingsAccount(mock, plan.AccountID, string(mustJSON(t, account.Extra)))
				mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM groups").WithArgs(int64(21), plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"exists", "replaced"}).AddRow(true, true))
			}
			mock.ExpectRollback()
			action, err := NewScheduledTestPlanRepository(db).ApplyQualityOutcome(context.Background(), plan, until, "passed")
			require.NoError(t, err)
			require.Equal(t, "restore_conflict", action)
			require.Equal(t, "manual_required", plan.QualityRestoreDiagnostic.Disposition, "group presence cannot claim the uncommitted model release")
			require.Equal(t, service.QualityActionIsolateModels, plan.QualityRestoreDiagnostic.Scope)
			if changedModel {
				require.Equal(t, "model_entry_changed", plan.QualityRestoreDiagnostic.Reason)
				require.Equal(t, []string{"sol"}, plan.QualityRestoreDiagnostic.Models)
			} else {
				require.Equal(t, "membership_already_present", plan.QualityRestoreDiagnostic.Reason)
			}
			require.Nil(t, plan.QualityModelActions)
			require.NoError(t, mock.ExpectationsWereMet(), "no model/group/ownership/outbox writes or commit")
		})
	}
}

func TestQualityRestoreDiagnosticsNativeModelAndConcurrencyGuards(t *testing.T) {
	for _, change := range []string{"entry", "concurrency"} {
		t.Run(change, func(t *testing.T) {
			now := time.Now()
			account := &service.Account{Type: service.AccountTypeOAuth, Concurrency: 20}
			state := &qualityState{}
			_, err := transitionQualityModels(account, state, 7, []string{"sol"}, now.Add(time.Hour), "failed", true, now)
			require.NoError(t, err)
			if change == "entry" {
				entries, ok := account.Extra["model_rate_limits"].(map[string]any)
				require.True(t, ok)
				entries["sol"] = map[string]any{"reason": "external-policy"}
			} else {
				account.Concurrency = 9
			}
			action, err := transitionQualityModels(account, state, 7, []string{"sol"}, now.Add(time.Hour), "passed", true, now)
			require.NoError(t, err)
			require.Equal(t, "restore_conflict", action)
			require.Equal(t, "manual_required", state.RestoreDiagnostic.Disposition)
			if change == "entry" {
				require.Equal(t, "model_entry_changed", state.RestoreDiagnostic.Reason)
				require.Contains(t, state.ModelApplied, "sol")
			} else {
				require.Equal(t, "concurrency_changed", state.RestoreDiagnostic.Reason)
				require.Equal(t, 5, *state.RestoreDiagnostic.ExpectedConcurrency)
				require.Equal(t, 9, *state.RestoreDiagnostic.CurrentConcurrency)
				require.Equal(t, 9, account.Concurrency, "foreign concurrency must not be overwritten")
			}
		})
	}
}

func TestQualityRestoreDiagnosticsBPSOnlyListsChangedManagedKeys(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
	}()
	plan := &service.ScheduledTestPlan{ID: 7, AccountID: 8, UpdatedAt: time.Now(), PelicanConfig: &service.PelicanTestConfig{Quality: &service.QualityPolicy{Action: service.QualityActionEnableBPS, AutoRestore: true}}}
	until := plan.UpdatedAt.Add(time.Minute)
	expectQualityDiagnosticAccount(mock, plan, until, `{"action":"enable_bps","pass_streak":1,"bps_applied":{"openai_excel_bps":true}}`, "active")
	mock.ExpectQuery("SELECT platform, type, parent_account_id").WithArgs(plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"platform", "type", "parent", "credentials", "extra"}).AddRow("openai", "oauth", nil, `{}`, `{"openai_excel_bps":false,"unrelated":"synthetic-private-value"}`))
	mock.ExpectCommit() // Existing BPS path may keep its pass streak, never touches the account.
	action, err := NewScheduledTestPlanRepository(db).ApplyQualityOutcome(context.Background(), plan, until, "passed")
	require.NoError(t, err)
	require.Equal(t, "restore_conflict", action)
	require.Equal(t, "bps_fields_changed", plan.QualityRestoreDiagnostic.Reason)
	require.Equal(t, []string{"openai_excel_bps"}, plan.QualityRestoreDiagnostic.ChangedFields)
	require.NotContains(t, string(mustJSON(t, plan.QualityRestoreDiagnostic)), "synthetic-private-value")
	require.NoError(t, mock.ExpectationsWereMet())
}
