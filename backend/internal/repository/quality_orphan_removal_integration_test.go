//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Three independent models: sol has a live orphan marker, astra's same-owner
// marker changed, and missing has a snapshot but no current marker.
func orphanRemovalFixture(t *testing.T) *qualityBPSFixture {
	t.Helper()
	f, _, _ := newQualityIsolationFixture(t)
	require.Equal(t, "groups_removed", isolationRound(f, "failed", "failed"))
	f.exec(`UPDATE accounts SET extra=jsonb_set(extra,'{quality_model_isolation,astra,isolated_at}','"2026-02-01T00:00:00Z"') WHERE id=$1`)
	_, err := integrationDB.Exec(`UPDATE account_quality_states SET state=jsonb_set(jsonb_set(jsonb_set(
  state #- '{models,model_applied,sol}' #- '{models,model_rate_limits,sol}',
  '{isolation_models}','["sol","astra","missing"]'),'{models,model_applied,missing}',
  jsonb_build_object('reason',$2::text,'isolated_at','2026-01-01T00:00:00Z')),'{models,model_rate_limits,missing}','null')
  || '{"failure_streak":4,"pass_streak":2,"quality_5xx_episode":9}'::jsonb WHERE plan_id=$1`, f.plan.ID, fmt.Sprintf("quality_rule:%d", f.plan.ID))
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE scheduled_test_plans SET pelican_config=jsonb_set(pelican_config,'{model_ids}','["sol","astra","missing"]') WHERE id=$1`, f.plan.ID)
	require.NoError(t, err)
	require.NoError(t, f.plans.FinishPelican(context.Background(), f.plan.ID, f.until, time.Now()))
	now := time.Now()
	for _, verdict := range []string{"success", "failed", "skipped"} {
		_, err = NewScheduledTestResultRepository(integrationDB).Create(context.Background(), &service.ScheduledTestResult{PlanID: f.plan.ID, QualityRoundID: "orphan-history-round", Status: verdict, PelicanConfig: f.plan.PelicanConfig, StartedAt: now, FinishedAt: now})
		require.NoError(t, err)
	}
	return f
}
func orphanPreview(t *testing.T, f *qualityBPSFixture) *service.QualityOwnershipPreview {
	t.Helper()
	p, err := f.plans.(service.QualityOwnershipRepository).QualityOwnership(context.Background(), f.plan.ID, nil)
	require.NoError(t, err)
	return p
}
func orphanConfirm(f *qualityBPSFixture, p *service.QualityOwnershipPreview, model string) error {
	_, err := f.plans.(service.QualityOwnershipRepository).QualityOwnership(context.Background(), f.plan.ID, &service.QualityOwnershipRequest{Fence: p.Fence, Confirm: true, RemoveModel: true, Model: model, ReasonCode: "orphan_marker_recovery"})
	return err
}
func orphanEntry(t *testing.T, p *service.QualityOwnershipPreview, model string) service.QualityModelRemoval {
	t.Helper()
	for _, e := range p.ModelRemovals {
		if e.Model == model {
			return e
		}
	}
	t.Fatalf("no model removal preview for %s", model)
	return service.QualityModelRemoval{}
}
func orphanSnapshot(t *testing.T, f *qualityBPSFixture) string {
	t.Helper()
	var data string
	require.NoError(t, integrationDB.QueryRow(`SELECT jsonb_build_object('account',to_jsonb(a),'plan',to_jsonb(p),'state',s.state,
 'memberships',(SELECT jsonb_agg(to_jsonb(g) ORDER BY group_id) FROM account_groups g WHERE g.account_id=a.id),
 'history',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY id),'[]') FROM scheduled_test_results r WHERE r.plan_id=p.id),
 'outbox',(SELECT COALESCE(jsonb_agg(to_jsonb(o) ORDER BY id),'[]') FROM scheduler_outbox o WHERE o.account_id=a.id))::text
 FROM accounts a JOIN scheduled_test_plans p ON p.account_id=a.id JOIN account_quality_states s ON s.plan_id=p.id WHERE p.id=$1`, f.plan.ID).Scan(&data))
	return data
}

func TestQualityOrphanRemovalAtomicScopeContraction(t *testing.T) {
	f := orphanRemovalFixture(t)
	ctx := context.Background()
	p := orphanPreview(t, f)
	require.True(t, orphanEntry(t, p, "sol").Ready)
	for _, entry := range p.Entries {
		if entry.Model == "sol" {
			require.Equal(t, "orphan_same_owner", entry.Status)
			require.Equal(t, "absent", entry.ExpectedOwner)
			require.Equal(t, "this_rule", entry.CurrentOwner)
		}
	}
	require.Equal(t, "remove_orphan_marker_only", orphanEntry(t, p, "sol").Effect)
	require.False(t, orphanEntry(t, p, "astra").Ready)
	require.Equal(t, "changed_same_owner", orphanEntry(t, p, "astra").BlockedReason)
	require.True(t, orphanEntry(t, p, "missing").Ready)
	before := orphanSnapshot(t, f)
	plan, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	plan.ModelID = "astra"
	plan.PelicanConfig.ModelIDs = []string{"astra", "missing"}
	_, err = f.plans.Update(ctx, plan)
	require.Equal(t, service.QualityScopeConflictReason, infraerrors.Reason(err))
	require.JSONEq(t, before, orphanSnapshot(t, f), "ordinary scope edits must roll back completely")
	require.NoError(t, orphanConfirm(f, p, "sol"))
	var a, b map[string]any
	require.NoError(t, json.Unmarshal([]byte(before), &a))
	require.NoError(t, json.Unmarshal([]byte(orphanSnapshot(t, f)), &b))
	// Compare the complete persisted aggregate after removing exactly the permitted
	// fields. This keeps the guard against accidental broad model/group changes.
	accountBefore := a["account"].(map[string]any)
	accountAfter := b["account"].(map[string]any)
	// Removing a configuration marker must invalidate existing AI proposals.
	require.Equal(t, accountBefore["ai_ops_revision"].(float64)+1, accountAfter["ai_ops_revision"])
	accountBefore["ai_ops_revision"] = accountAfter["ai_ops_revision"]
	delete(accountBefore, "updated_at")
	delete(accountAfter, "updated_at")
	delete(accountBefore["extra"].(map[string]any)[service.QualityModelIsolationKey].(map[string]any), "sol")
	planBefore := a["plan"].(map[string]any)
	planAfter := b["plan"].(map[string]any)
	require.NotEqual(t, planBefore["updated_at"], planAfter["updated_at"])
	delete(planBefore, "updated_at")
	delete(planAfter, "updated_at")
	planBefore["model_id"] = "astra"
	planBefore["pelican_config"].(map[string]any)["model_ids"] = []any{"astra", "missing"}
	a["state"].(map[string]any)["isolation_models"] = []any{"astra", "missing"}
	oldOutbox := a["outbox"].([]any)
	newOutbox := b["outbox"].([]any)
	require.Len(t, newOutbox, len(oldOutbox)+1)
	require.Equal(t, oldOutbox, newOutbox[:len(oldOutbox)])
	require.Equal(t, "account_changed", newOutbox[len(oldOutbox)].(map[string]any)["event_type"])
	delete(a, "outbox")
	delete(b, "outbox")
	require.Equal(t, a, b, "all other account, policy, ownership, groups, counters and history must be identical")
	require.Equal(t, "stale_run", isolationRound(f, "failed", "failed"))
	fresh := orphanPreview(t, f)
	require.NotContains(t, fresh.Models, "sol")
	require.NoError(t, orphanConfirm(f, fresh, "missing"), "missing target can also be removed without abandoning changed astra")
	plan, err = f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	plan.CronExpression = "0 * * * *"
	_, err = f.plans.Update(ctx, plan)
	require.NoError(t, err, "remaining managed model must not block saving the reduced scope")
	f.claim()
	f.plan.Quality5xxEpisode = 9
	f.plan.QualityModelOutcomes = map[string]string{"astra": "failed", "sol": "failed"}
	require.Equal(t, "action_conflict", f.apply("failed"), "changed astra is retained, not auto abandoned")
	require.NotContains(t, f.extra()[service.QualityModelIsolationKey], "sol", "even stale result metadata cannot reapply removed model")
	// Resolving astra's manual conflict independently allows a real later failed
	// round; only the configured remaining model can be isolated.
	var saved string
	require.NoError(t, integrationDB.QueryRow(`SELECT state->'models'->'model_applied'->'astra' FROM account_quality_states WHERE plan_id=$1`, f.plan.ID).Scan(&saved))
	f.exec(`UPDATE accounts SET extra=jsonb_set(extra,'{quality_model_isolation,astra}',$2::jsonb) WHERE id=$1`, saved)
	require.Equal(t, "models_isolated", f.apply("failed"))
	require.NotContains(t, f.extra()[service.QualityModelIsolationKey], "sol")
}

func TestQualityOrphanRemovalRejectsUnsafeCurrentEntries(t *testing.T) {
	for _, kind := range []string{"other_owner", "external", "no_owner", "malformed", "null_owner", "additional", "changed_same_owner", "previous", "shared_mapping"} {
		t.Run(kind, func(t *testing.T) {
			f := orphanRemovalFixture(t)
			switch kind {
			case "other_owner":
				f.exec(`UPDATE accounts SET extra=jsonb_set(extra,'{quality_model_isolation,sol}','{"reason":"quality_rule:999","isolated_at":"2026-01-01T00:00:00Z"}') WHERE id=$1`)
			case "external":
				f.exec(`UPDATE accounts SET extra=jsonb_set(extra,'{quality_model_isolation,sol}','{"reason":"manual","isolated_at":"2026-01-01T00:00:00Z"}') WHERE id=$1`)
			case "no_owner":
				f.exec(`UPDATE accounts SET extra=jsonb_set(extra,'{quality_model_isolation,sol}','{"isolated_at":"2026-01-01T00:00:00Z"}') WHERE id=$1`)
			case "null_owner":
				f.exec(`UPDATE accounts SET extra=jsonb_set(extra,'{quality_model_isolation,sol}','{"reason":null}') WHERE id=$1`)
			case "malformed":
				f.exec(`UPDATE accounts SET extra=jsonb_set(extra,'{quality_model_isolation,sol}','[]') WHERE id=$1`)
			case "additional":
				f.exec(`UPDATE accounts SET extra=jsonb_set(extra,'{quality_model_isolation,sol,allowed_models}','["restricted"]') WHERE id=$1`)
			case "changed_same_owner":
				_, err := integrationDB.Exec(`UPDATE account_quality_states SET state=jsonb_set(state,'{models,model_applied,sol}',jsonb_build_object('reason',$2::text,'isolated_at','2020-01-01T00:00:00Z')) WHERE plan_id=$1`, f.plan.ID, fmt.Sprintf("quality_rule:%d", f.plan.ID))
				require.NoError(t, err)
			case "previous":
				_, err := integrationDB.Exec(`UPDATE account_quality_states SET state=jsonb_set(state,'{models,model_rate_limits,sol}','{"reason":"manual"}') WHERE plan_id=$1`, f.plan.ID)
				require.NoError(t, err)
			case "shared_mapping":
				f.exec(`UPDATE accounts SET credentials='{"model_mapping":{"astra":"sol"}}' WHERE id=$1`)
			}
			p := orphanPreview(t, f)
			for _, entry := range p.Entries {
				if entry.Model == "sol" {
					switch kind {
					case "other_owner":
						require.Equal(t, "other_rule", entry.CurrentOwner)
						require.Equal(t, int64(999), entry.CurrentOwnerRuleID)
					case "external":
						require.Equal(t, "external", entry.CurrentOwner)
					case "no_owner", "null_owner", "malformed":
						require.Equal(t, "unrecognized", entry.CurrentOwner)
					}
				}
			}
			require.False(t, orphanEntry(t, p, "sol").Ready)
			before := orphanSnapshot(t, f)
			require.Equal(t, "QUALITY_MODEL_REMOVAL_BLOCKED", infraerrors.Reason(orphanConfirm(f, p, "sol")))
			require.JSONEq(t, before, orphanSnapshot(t, f))
		})
	}
}

func TestQualityOrphanRemovalRevalidatesFullPreviewAndLease(t *testing.T) {
	for _, kind := range []string{"version", "account", "state", "entry", "mapping", "lease", "group"} {
		t.Run(kind, func(t *testing.T) {
			f := orphanRemovalFixture(t)
			p := orphanPreview(t, f)
			switch kind {
			case "version":
				_, err := integrationDB.Exec(`UPDATE scheduled_test_plans SET updated_at=clock_timestamp() WHERE id=$1`, f.plan.ID)
				require.NoError(t, err)
			case "account":
				f.exec(`UPDATE accounts SET updated_at=clock_timestamp() WHERE id=$1`)
			case "entry":
				f.exec(`UPDATE accounts SET extra=jsonb_set(extra,'{quality_model_isolation,sol,isolated_at}','"2026-01-02T00:00:00Z"') WHERE id=$1`)
			case "state":
				_, err := integrationDB.Exec(`UPDATE account_quality_states SET state=jsonb_set(state,'{pass_streak}','8') WHERE plan_id=$1`, f.plan.ID)
				require.NoError(t, err)
			case "mapping":
				f.exec(`UPDATE accounts SET credentials='{"model_mapping":{"sol":"new-key"}}' WHERE id=$1`)
			case "lease":
				_, err := integrationDB.Exec(`UPDATE scheduled_test_plans SET running_until=NOW()+interval '5 minutes' WHERE id=$1`, f.plan.ID)
				require.NoError(t, err)
			case "group":
				f.exec(`UPDATE account_groups SET priority=99 WHERE account_id=$1`)
			}
			before := orphanSnapshot(t, f)
			require.Equal(t, "QUALITY_OWNERSHIP_PREVIEW_CHANGED", infraerrors.Reason(orphanConfirm(f, p, "sol")))
			require.JSONEq(t, before, orphanSnapshot(t, f))
			if kind == "lease" {
				fresh := orphanPreview(t, f)
				require.Equal(t, "rule_running", orphanEntry(t, fresh, "sol").BlockedReason)
				require.Error(t, orphanConfirm(f, fresh, "sol"))
			}
		})
	}
}

func TestQualityOrphanRemovalConcurrentConfirmations(t *testing.T) {
	f := orphanRemovalFixture(t)
	p := orphanPreview(t, f)
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- orphanConfirm(f, p, "sol") }()
	}
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else {
			require.Equal(t, "QUALITY_OWNERSHIP_PREVIEW_CHANGED", infraerrors.Reason(err))
		}
	}
	require.Equal(t, 1, successes)
}

func TestQualityOrphanRemovalMatchingAndMappedMarkers(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		t.Run(fmt.Sprint(mapped), func(t *testing.T) {
			f, _, _ := newQualityIsolationFixture(t)
			require.Equal(t, "groups_removed", isolationRound(f, "failed", "failed"))
			require.NoError(t, f.plans.FinishPelican(context.Background(), f.plan.ID, f.until, time.Now()))
			target := "sol"
			if mapped {
				f.exec(`UPDATE accounts SET credentials='{"model_mapping":{"public-sol":"sol"}}' WHERE id=$1`)
				_, err := integrationDB.Exec(`UPDATE scheduled_test_plans SET model_id='public-sol',pelican_config=jsonb_set(pelican_config,'{model_ids}','["public-sol","astra"]') WHERE id=$1`, f.plan.ID)
				require.NoError(t, err)
				_, err = integrationDB.Exec(`UPDATE account_quality_states SET state=jsonb_set(state,'{isolation_models}','["public-sol","astra"]') WHERE plan_id=$1`, f.plan.ID)
				require.NoError(t, err)
				target = "public-sol"
			}
			p := orphanPreview(t, f)
			e := orphanEntry(t, p, target)
			require.True(t, e.Ready)
			require.Equal(t, "remove_matching_marker", e.Effect)
			require.Equal(t, "sol", e.IsolationModel)
			require.NoError(t, orphanConfirm(f, p, target))
			require.NotContains(t, f.extra()[service.QualityModelIsolationKey], "sol")
			require.Contains(t, f.extra()[service.QualityModelIsolationKey], "astra")
		})
	}
}

// Reproduce the reported model identities with synthetic rule/account IDs. No
// provider request, production account or copied credential is used by this test.
func TestQualityOrphanRule152ModelsAndStaleClaims(t *testing.T) {
	f := orphanRemovalFixture(t)
	f.exec(`UPDATE accounts SET extra=jsonb_set(extra,'{quality_model_isolation}',jsonb_build_object(
  'gpt-5.6-sol',extra->'quality_model_isolation'->'sol',
  'gpt-6-astra',extra->'quality_model_isolation'->'astra'))
  || '{"model_rate_limits":{"gpt-5.6-sol":{"reason":"native-independent","rate_limit_reset_at":"2099-01-01T00:00:00Z"}},"unrelated_setting":"preserved"}'::jsonb WHERE id=$1`)
	_, err := integrationDB.Exec(`UPDATE account_quality_states SET state=jsonb_set(jsonb_set(jsonb_set(state,
  '{isolation_models}','["gpt-6-astra","gpt-6.1-sol","gpt-5.6-sol"]'),
  '{models,model_applied}',jsonb_build_object('gpt-6-astra',state->'models'->'model_applied'->'astra','gpt-6.1-sol',state->'models'->'model_applied'->'missing')),
  '{models,model_rate_limits}','{"gpt-6-astra":null,"gpt-6.1-sol":null}') WHERE plan_id=$1`, f.plan.ID)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE scheduled_test_plans SET model_id='gpt-6-astra',pelican_config=jsonb_set(pelican_config,'{model_ids}','["gpt-6-astra","gpt-6.1-sol","gpt-5.6-sol"]') WHERE id=$1`, f.plan.ID)
	require.NoError(t, err)
	ctx := context.Background()
	old, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	p := orphanPreview(t, f)
	require.True(t, orphanEntry(t, p, "gpt-5.6-sol").Ready)
	require.Equal(t, "remove_orphan_marker_only", orphanEntry(t, p, "gpt-5.6-sol").Effect)
	require.Equal(t, "changed_same_owner", orphanEntry(t, p, "gpt-6-astra").BlockedReason)
	require.True(t, orphanEntry(t, p, "gpt-6.1-sol").Ready)
	require.NoError(t, orphanConfirm(f, p, "gpt-5.6-sol"))
	require.NotContains(t, f.extra()[service.QualityModelIsolationKey], "gpt-5.6-sol")
	require.Contains(t, f.extra()["model_rate_limits"], "gpt-5.6-sol", "native restrictions must survive isolation release")
	require.Contains(t, f.extra()[service.QualityModelIsolationKey], "gpt-6-astra")
	// A previously read scheduler candidate cannot claim the old configuration.
	// Also test an expired worker's outcome under its pre-confirmation version.
	now := old.NextRunAt.Add(time.Second).Truncate(time.Microsecond)
	claimed, err := f.plans.ClaimPelican(ctx, old, now, now.Add(time.Minute), now.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, claimed)
	fresh, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	require.Equal(t, "gpt-6-astra", fresh.ModelID, "an unrelated primary model stays unchanged")
	require.Equal(t, []string{"gpt-6-astra", "gpt-6.1-sol"}, service.QualityModels(fresh))
	claimed, err = f.plans.ClaimPelican(ctx, fresh, now, now.Add(time.Minute), now.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, claimed, "at the same due time the fresh version can claim")
	_, err = f.plans.Update(ctx, fresh)
	require.NoError(t, err, "the narrowed scope remains editable with managed peers")
}
