//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var confirmedProbeFixtures sync.Map

// Legacy account-action tests explicitly enter the existing trusted probe/candy
// confirmation API. HTTP pressure no longer authorizes their account-wide action.
// This fixture is not used by independent-429 worker tests as run evidence.
func prepareConfirmedProbeFixture(t *testing.T, id int64) service.OAuthDegradationCandyReview {
	t.Helper()
	if saved, ok := confirmedProbeFixtures.Load(id); ok {
		return saved.(service.OAuthDegradationCandyReview)
	}
	ctx := t.Context()
	var raw string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, service.SettingKeyOAuthAutoConfig).Scan(&raw))
	var cfg service.OAuthAutoConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &cfg))
	if !cfg.DegradationAction.UsesCandyRetest() {
		return service.OAuthDegradationCandyReview{}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	review := service.OAuthDegradationCandyReview{ModelID: "gpt-6-luna", LeaseUntil: now.Add(10 * time.Minute), StartedAt: now, Incorrect: 2, Revision: cfg.Revision}
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config,updated_at,running_until)
 VALUES($1,$2,'*/30 * * * *',true,'{"quality":{"action":"observe_only"}}',$3,$4) RETURNING id,updated_at`, id, review.ModelID, now.Add(-time.Minute), review.LeaseUntil).Scan(&review.PlanID, &review.PlanUpdatedAt))
	// Keep the separate action-confirmation fixture out of the performance sample
	// denominator; these tests supply their own precise quality-window samples.
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message)
 VALUES($1,'failed',$2,$2,$2,jsonb_build_object('model_id',$3::text),$4,'comparison_pending','{"verdict":"incorrect"}','answer_mismatch')`, review.PlanID, now, review.ModelID, review.LeaseUntil.Format(time.RFC3339Nano))
	require.NoError(t, err)
	confirmedProbeFixtures.Store(id, review)
	t.Cleanup(func() {
		confirmedProbeFixtures.Delete(id)
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM scheduled_test_plans WHERE id=$1`, review.PlanID)
		require.NoError(t, err)
	})
	return review
}
func evaluateConfirmedProbe(ctx context.Context, repo *accountRepository, id int64, now time.Time) error {
	value, ok := confirmedProbeFixtures.Load(id)
	if !ok {
		return repo.EvaluateOAuthDegradation(ctx, id, now)
	}
	review := value.(service.OAuthDegradationCandyReview)
	return repo.evaluateOAuthDegradation(ctx, id, now, &review)
}

type confirmedProbeAccounts struct{ *accountRepository }

func (r confirmedProbeAccounts) EvaluateOAuthDegradation(ctx context.Context, id int64, now time.Time) error {
	return evaluateConfirmedProbe(ctx, r.accountRepository, id, now)
}

func TestOAuthDegradationProbeFixturePreservesOtherScopeLease(t *testing.T) {
	_, _ = degradationPolicyFixture(t)
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: service.QualityActionRemoveModel, RemoveModels: []string{"gpt-6-astra"}})
	review := prepareConfirmedProbeFixture(t, f.account)
	require.NotEqual(t, f.plan.ID, review.PlanID, "confirmation uses the independent observation scope")
	var lease time.Time
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT running_until FROM scheduled_test_plans WHERE id=$1`, f.plan.ID).Scan(&lease))
	require.True(t, f.until.Equal(lease), "prepare must not replace the quarantine rule's current lease")
	var plans int
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT count(*) FROM scheduled_test_plans WHERE account_id=$1`, f.account).Scan(&plans))
	require.Equal(t, 2, plans)
}
