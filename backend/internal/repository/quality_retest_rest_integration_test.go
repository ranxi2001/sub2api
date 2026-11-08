//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func qualityRestDeadline(t *testing.T, id int64) *time.Time {
	t.Helper()
	var raw []byte
	require.NoError(t, integrationDB.QueryRow(`SELECT state FROM account_quality_states WHERE plan_id=$1`, id).Scan(&raw))
	var state qualityState
	require.NoError(t, json.Unmarshal(raw, &state))
	return state.RetestAfter
}

func TestQualityRetestRestBlocksEveryClaimUntilDeadline(t *testing.T) {
	ctx := context.Background()
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: "disable_scheduling", RetestRestMinutes: 60, AutoRestore: true, TriggerOnUpstream5xx: true})
	require.Equal(t, "scheduling_disabled", f.apply("failed"))
	deadline := qualityRestDeadline(t, f.plan.ID)
	require.NotNil(t, deadline)
	require.WithinDuration(t, time.Now().Add(time.Hour), *deadline, 5*time.Second)
	var next time.Time
	require.NoError(t, integrationDB.QueryRow(`SELECT next_run_at FROM scheduled_test_plans WHERE id=$1`, f.plan.ID).Scan(&next))
	require.False(t, next.Before(*deadline))
	require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, f.until, time.Now()))
	require.ErrorContains(t, f.plans.TriggerQuality(ctx, f.plan.ID), "rest period")
	// A rule edit/queued trigger can make the cron due again. Neither that nor
	// a new repository instance or a 5xx signal may bypass the durable deadline.
	_, err := integrationDB.Exec(`UPDATE scheduled_test_plans SET next_run_at=NOW()-interval '1 minute' WHERE id=$1`, f.plan.ID)
	require.NoError(t, err)
	for _, source := range []string{"", "upstream_5xx"} {
		restarted := NewScheduledTestPlanRepository(integrationDB)
		plan, err := restarted.GetByID(ctx, f.plan.ID)
		require.NoError(t, err)
		plan.TriggerSource = source
		now := time.Now().Add(time.Second).Truncate(time.Microsecond)
		ok, err := restarted.ClaimPelican(ctx, plan, now, now.Add(15*time.Minute), now.Add(30*time.Minute))
		require.NoError(t, err)
		require.False(t, ok, "all execution sources must respect quality rest")
	}
	_, err = integrationDB.Exec(`UPDATE account_quality_states SET state=jsonb_set(state,'{retest_after}','"2020-01-01T00:00:00Z"') WHERE plan_id=$1`, f.plan.ID)
	require.NoError(t, err)
	require.Equal(t, 0, f.count(`SELECT count(*) FROM accounts WHERE id=$1 AND schedulable`), "expiry cannot restore the account")
	f.claim()
	require.Equal(t, "restored", f.apply("passed"))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM accounts WHERE id=$1 AND schedulable`))
}

func TestQualityRetestRestUnknownDoesNotExtendAndZeroKeepsSchedule(t *testing.T) {
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: "disable_scheduling", RetestRestMinutes: 60})
	require.Equal(t, "inconclusive", f.apply("inconclusive"))
	require.Zero(t, f.count(`SELECT count(*) FROM account_quality_states WHERE plan_id=(SELECT id FROM scheduled_test_plans WHERE account_id=$1)`))
	require.Equal(t, "scheduling_disabled", f.apply("failed"))
	deadline := qualityRestDeadline(t, f.plan.ID)
	require.Equal(t, "inconclusive", f.apply("inconclusive"))
	require.Equal(t, deadline, qualityRestDeadline(t, f.plan.ID))
	require.Equal(t, "passed", f.apply("passed"))
	require.Equal(t, 0, f.count(`SELECT count(*) FROM accounts WHERE id=$1 AND schedulable`), "auto restore remains opt-in")
	require.Equal(t, "already_quarantined", f.apply("failed"))
	require.True(t, qualityRestDeadline(t, f.plan.ID).After(*deadline), "a new confirmed failure starts a new rest")

	legacy := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: "disable_scheduling"})
	require.Equal(t, "scheduling_disabled", legacy.apply("failed"))
	require.Nil(t, qualityRestDeadline(t, legacy.plan.ID))
	require.NoError(t, legacy.plans.FinishPelican(context.Background(), legacy.plan.ID, legacy.until, time.Now()))
	legacy.claim()
}

func TestQualityRetestRestUsesIndependentModelVerdicts(t *testing.T) {
	f, _, _ := newQualityIsolationFixture(t)
	_, err := integrationDB.Exec(`UPDATE scheduled_test_plans SET pelican_config=jsonb_set(pelican_config,'{quality,retest_rest_minutes}','75') WHERE id=$1`, f.plan.ID)
	require.NoError(t, err)
	f.plan, err = f.plans.GetByID(context.Background(), f.plan.ID)
	require.NoError(t, err)
	f.plan.QualityModelOutcomes = map[string]string{"sol": "failed", "astra": "passed"}
	require.Equal(t, "models_isolated", f.apply("inconclusive"))
	deadline := qualityRestDeadline(t, f.plan.ID)
	require.NotNil(t, deadline)
	require.WithinDuration(t, time.Now().Add(75*time.Minute), *deadline, 5*time.Second)
	account := &service.Account{Platform: service.PlatformOpenAI, Status: "active", Schedulable: true, Extra: f.extra()}
	require.False(t, account.IsSchedulableForModel("sol"))
	require.True(t, account.IsSchedulableForModel("astra"), "resting the test rule must not quarantine a healthy model")
	f.plan.PelicanConfig.Quality.AutoRestore = false
	f.plan.QualityModelOutcomes = map[string]string{"sol": "passed", "astra": "passed"}
	f.apply("failed") // Per-model evidence takes precedence over the aggregate.
	require.Equal(t, deadline, qualityRestDeadline(t, f.plan.ID))
}

func TestQualityRetestRestLegacyModelCooldown(t *testing.T) {
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: service.QualityActionRemoveModel,
		RemoveModels: []string{"gpt-6-astra"}, RetestRestMinutes: 90, AutoRestore: true})
	require.Equal(t, "models_cooled", f.apply("failed"))
	require.NotNil(t, qualityRestDeadline(t, f.plan.ID))
	require.WithinDuration(t, time.Now().Add(90*time.Minute), *qualityRestDeadline(t, f.plan.ID), 5*time.Second)
	require.NoError(t, f.plans.FinishPelican(context.Background(), f.plan.ID, f.until, time.Now()))
	require.ErrorContains(t, f.plans.TriggerQuality(context.Background(), f.plan.ID), "rest period")
}
