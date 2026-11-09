//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuthDegradationQualityRestoreKeepsClosedGroup(t *testing.T) {
	f, closed, kept := newQualityIsolationFixture(t)
	require.Equal(t, "groups_removed", isolationRound(f, "failed", "failed"))
	f.exec(`UPDATE accounts SET extra=jsonb_set(extra,'{oauth_degradation_action}',jsonb_build_object('limit_concurrency',false,'limit_models',false,'limit_groups',true,'group_ids',jsonb_build_array($2::bigint))) WHERE id=$1`, closed)
	var beforeState, beforeExtra string
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT state::text FROM account_quality_states WHERE plan_id=$1", f.plan.ID).Scan(&beforeState))
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT extra::text FROM accounts WHERE id=$1", f.account).Scan(&beforeExtra))
	beforeEvents := f.count("SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1")
	f.plans = NewScheduledTestPlanRepository(integrationDB)
	var err error
	f.plan, err = f.plans.GetByID(t.Context(), f.plan.ID)
	require.NoError(t, err)
	require.Equal(t, "restore_conflict", isolationRound(f, "passed", "passed"))
	require.NotNil(t, f.plan.QualityRestoreDiagnostic)
	require.Equal(t, "oauth_degradation_group_limited", f.plan.QualityRestoreDiagnostic.Reason)
	require.Zero(t, f.count("SELECT COUNT(*) FROM account_groups WHERE account_id=$1 AND group_id="+qualityIsolationID(closed)))
	require.Equal(t, 1, f.count("SELECT COUNT(*) FROM account_groups WHERE account_id=$1 AND group_id="+qualityIsolationID(kept)))
	var afterState, afterExtra string
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT state::text FROM account_quality_states WHERE plan_id=$1", f.plan.ID).Scan(&afterState))
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT extra::text FROM accounts WHERE id=$1", f.account).Scan(&afterExtra))
	require.JSONEq(t, beforeState, afterState, "conflicting recovery must retain durable ownership")
	require.JSONEq(t, beforeExtra, afterExtra, "model recovery and group restore must roll back together")
	require.Equal(t, beforeEvents, f.count("SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1"))
}

func TestOAuthDegradationScopedAdminServiceCannotRebindClosedGroup(t *testing.T) {
	repo, _ := degradationPolicyFixture(t)
	a := degradationAccountFixture(t, 20, nil, nil, time.Minute)
	groups := make([]*service.Group, 3)
	for i := range groups {
		groups[i] = mustCreateGroup(t, integrationEntClient, &service.Group{Name: fmt.Sprintf("scoped-degradation-%d-%d", a.ID, i), Platform: service.PlatformOpenAI})
	}
	t.Cleanup(func() {
		for _, group := range groups {
			require.NoError(t, integrationEntClient.Group.DeleteOneID(group.ID).Exec(context.Background()))
		}
	})
	closed, kept, foreign := groups[0].ID, groups[1].ID, groups[2].ID
	require.NoError(t, repo.BindGroups(t.Context(), a.ID, []int64{kept, foreign}))
	stamp := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	_, err := integrationDB.ExecContext(t.Context(), `UPDATE account_groups SET priority=73,created_at=$3,allowed_models='["gpt-6-luna"]' WHERE account_id=$1 AND group_id=$2`, a.ID, foreign, stamp)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(t.Context(), `UPDATE accounts SET extra=jsonb_set(extra,'{oauth_degradation_action}',jsonb_build_object('limit_concurrency',false,'limit_models',false,'limit_groups',true,'group_ids',jsonb_build_array($2::bigint))) WHERE id=$1`, a.ID, closed)
	require.NoError(t, err)
	adminRepo := NewAdminAccountRepository(integrationEntClient, integrationDB, nil)
	adminGroups := NewAdminGroupRepository(integrationEntClient, integrationDB)
	admin := service.NewAdminService(nil, nil, adminGroups, adminRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, integrationEntClient, nil, nil, nil, nil, nil, nil, nil, nil, nil).(service.ScopedAdminAccounts)
	principal := &adminaccess.Principal{CreatorID: 9, Policy: adminaccess.Policy{Rules: []adminaccess.Rule{{Permissions: []string{"accounts.groups"}, GroupIDs: []int64{closed, kept}, AccountTypes: []string{"oauth"}, Platforms: []string{"openai"}}}}}
	raw, err := json.Marshal([]int64{closed, kept})
	require.NoError(t, err)
	fields := map[string]json.RawMessage{"group_ids": raw}
	ctx := adminaccess.WithPrincipal(t.Context(), principal)
	_, err = admin.ScopedAdminWrite(ctx, a.ID, "", fields, nil)
	require.NoError(t, err, "authorized stale form must pass through actual service permission and transaction chain")
	after, err := repo.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{kept, foreign}, after.GroupIDs)
	require.True(t, after.HasOAuthDegradationAction())
	var priority int
	var created time.Time
	var models string
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT priority,created_at,allowed_models::text FROM account_groups WHERE account_id=$1 AND group_id=$2", a.ID, foreign).Scan(&priority, &created, &models))
	require.Equal(t, 73, priority)
	require.Equal(t, stamp, created)
	require.JSONEq(t, `["gpt-6-luna"]`, models)
	denied := &adminaccess.Principal{CreatorID: 9, Policy: adminaccess.Policy{Rules: []adminaccess.Rule{{Permissions: []string{"accounts.read"}, GroupIDs: []int64{kept}, AccountTypes: []string{"oauth"}}}}}
	_, err = admin.ScopedAdminWrite(adminaccess.WithPrincipal(t.Context(), denied), a.ID, "", fields, nil)
	require.Equal(t, 403, infraerrors.Code(err))
	outOfScope := &adminaccess.Principal{CreatorID: 9, Policy: adminaccess.Policy{Rules: []adminaccess.Rule{{Permissions: []string{"accounts.groups"}, GroupIDs: []int64{closed}, AccountTypes: []string{"oauth"}, Platforms: []string{"openai"}}}}}
	_, err = admin.ScopedAdminWrite(adminaccess.WithPrincipal(t.Context(), outOfScope), a.ID, "", fields, nil)
	require.Equal(t, 404, infraerrors.Code(err), "a closed grant alone must not expose the account")
	final, err := repo.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	require.Equal(t, after.GroupIDs, final.GroupIDs)
	require.Equal(t, after.Extra, final.Extra)
	require.Equal(t, after.UpdatedAt, final.UpdatedAt, "denied request must not mutate storage")
}

func TestOAuthDegradationGroupTransactionFailures(t *testing.T) {
	for _, table := range []string{"scheduler_outbox", "account_auto_config_events"} {
		t.Run(table, func(t *testing.T) {
			repo, policy := degradationPolicyFixture(t)
			a := degradationAccountFixture(t, 20, nil, []string{"incorrect"}, time.Minute)
			group := mustCreateGroup(t, integrationEntClient, &service.Group{Name: fmt.Sprintf("group-rollback-%d", a.ID), Platform: service.PlatformOpenAI})
			t.Cleanup(func() {
				require.NoError(t, integrationEntClient.Group.DeleteOneID(group.ID).Exec(context.Background()))
			})
			require.NoError(t, repo.BindGroups(t.Context(), a.ID, []int64{group.ID}))
			require.NoError(t, repo.SetGroupAllowedModels(t.Context(), a.ID, map[int64][]string{group.ID: {"gpt-6-luna"}}))
			policy.DegradationAction.LimitGroups, policy.DegradationAction.GroupIDs = true, []int64{group.ID}
			raw, err := json.Marshal(policy)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(t.Context(), "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, string(raw))
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(t.Context(), "DELETE FROM scheduler_outbox WHERE account_id=$1", a.ID)
			require.NoError(t, err)
			before, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			fn := fmt.Sprintf("degradation_group_fail_%d", a.ID)
			_, err = integrationDB.ExecContext(t.Context(), fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.account_id=%d THEN RAISE EXCEPTION 'degradation group transaction fault'; END IF; RETURN NEW; END $$`, fn, a.ID))
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP FUNCTION IF EXISTS %s() CASCADE", fn))
				require.NoError(t, err)
			})
			_, err = integrationDB.ExecContext(t.Context(), fmt.Sprintf("CREATE TRIGGER degradation_group_fault BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION %s()", table, fn))
			require.NoError(t, err)
			require.ErrorContains(t, repo.EvaluateOAuthDegradation(t.Context(), a.ID, time.Now()), "degradation group transaction fault")
			after, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			require.Equal(t, before.GroupIDs, after.GroupIDs, "removed membership must roll back")
			require.Equal(t, before.GroupAllowedModels(group.ID), after.GroupAllowedModels(group.ID))
			require.Equal(t, before.Extra, after.Extra)
			require.Equal(t, before.Concurrency, after.Concurrency)
			require.Equal(t, before.UpdatedAt, after.UpdatedAt)
			history, outbox := degradationRows(t, a.ID)
			require.Zero(t, history)
			require.Zero(t, outbox)
			t.Logf("real PostgreSQL %s fault rolled back group membership, models, concurrency, marker, history and both outbox events", table)
		})
	}
}

func TestOAuthDegradationAstraRestoreKeepsClosedGroup(t *testing.T) {
	for _, leaveMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("leave-mode-%v", leaveMode), func(t *testing.T) {
			f := newAstraGroupFixture(t)
			f.exec(`INSERT INTO account_groups(account_id,group_id,priority,allowed_models) VALUES($1,$2,7,'["gpt-5"]')`, f.id, f.selected)
			kept := f.groups()[f.other]
			f.apply(false, true)
			f.exec(`UPDATE accounts SET extra=jsonb_set(extra,'{oauth_degradation_action}',jsonb_build_object('limit_concurrency',false,'limit_models',false,'limit_groups',true,'group_ids',jsonb_build_array($2::bigint))) WHERE id=$1`, f.id, f.selected)
			var before, after string
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT state::text FROM astra_scheduling_states WHERE account_id=$1", f.id).Scan(&before))
			var events int
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1", f.id).Scan(&events))
			f.repo = newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
			if leaveMode {
				f.settings.SchedulingMode = "model"
				f.save()
			}
			_, err := f.repo.ApplyAstraScheduling(t.Context(), f.id, f.settings, true)
			require.ErrorContains(t, err, "astra_restore_conflict")
			require.NotContains(t, f.groups(), f.selected)
			require.Equal(t, kept, f.groups()[f.other], "unrelated membership and model restrictions survive")
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT state::text FROM astra_scheduling_states WHERE account_id=$1", f.id).Scan(&after))
			require.JSONEq(t, before, after)
			var afterEvents int
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1", f.id).Scan(&afterEvents))
			require.Equal(t, events, afterEvents)
		})
	}
}

func TestOAuthDegradationGroupBindingEnteredFence(t *testing.T) {
	repo, policy := degradationPolicyFixture(t)
	a := degradationAccountFixture(t, 20, nil, []string{"incorrect"}, time.Minute)
	closed := mustCreateGroup(t, integrationEntClient, &service.Group{Name: fmt.Sprintf("closed-fence-%d", a.ID), Platform: service.PlatformOpenAI})
	kept := mustCreateGroup(t, integrationEntClient, &service.Group{Name: fmt.Sprintf("kept-fence-%d", a.ID), Platform: service.PlatformOpenAI})
	t.Cleanup(func() {
		for _, id := range []int64{closed.ID, kept.ID} {
			require.NoError(t, integrationEntClient.Group.DeleteOneID(id).Exec(context.Background()))
		}
	})
	require.NoError(t, repo.BindGroups(t.Context(), a.ID, []int64{closed.ID, kept.ID}))
	require.NoError(t, repo.SetGroupAllowedModels(t.Context(), a.ID, map[int64][]string{kept.ID: {"gpt-6-luna"}}))
	policy.DegradationAction.LimitConcurrency = degradationActionFlag(false)
	policy.DegradationAction.LimitModels = degradationActionFlag(false)
	policy.DegradationAction.LimitGroups, policy.DegradationAction.GroupIDs = true, []int64{closed.ID}
	raw, err := json.Marshal(policy)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(t.Context(), "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, string(raw))
	require.NoError(t, err)
	one, _, onePID := degradationReplica(t)
	two, _, twoPID := degradationReplica(t)
	gate := testTx(t)
	gatePID := degradationGatePID(t, gate)
	_, err = gate.ExecContext(t.Context(), "SELECT id FROM accounts WHERE id=$1 FOR UPDATE", a.ID)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	action, editor := make(chan error, 1), make(chan error, 1)
	go func() { action <- one.EvaluateOAuthDegradation(ctx, a.ID, time.Now()) }()
	degradationEntered(t, onePID, "%FROM accounts%FOR UPDATE%", gatePID)
	go func() { editor <- two.BindGroups(ctx, a.ID, []int64{closed.ID, kept.ID}) }()
	degradationEntered(t, twoPID, "%FROM accounts%FOR NO KEY UPDATE%", 0)
	t.Log("release: evaluator and stale membership editor both entered real PostgreSQL account-lock waits")
	require.NoError(t, gate.Commit())
	require.NoError(t, <-action)
	require.NoError(t, <-editor)
	after, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{kept.ID}, after.GroupIDs)
	require.Equal(t, []string{"gpt-6-luna"}, after.GroupAllowedModels(kept.ID))
	require.Equal(t, 20, after.Concurrency)
	history, _ := degradationRows(t, a.ID)
	require.Equal(t, 1, history)
}
