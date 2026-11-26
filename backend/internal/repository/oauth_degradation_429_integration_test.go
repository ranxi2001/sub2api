//go:build integration

package repository

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuthDegradationCandyReviewUsesCurrentRule(t *testing.T) {
	repo, c := degradationPolicyFixture(t)
	a := degradationAccountFixture(t, 20, nil, nil, time.Minute)
	ctx := t.Context()
	now := time.Now()
	review := service.OAuthDegradationCandyReview{ModelID: "gpt-6-luna", LeaseUntil: now.Add(time.Minute).Truncate(time.Microsecond), StartedAt: now.Add(-time.Minute)}
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config,updated_at,running_until)
 VALUES($1,$2,'*/30 * * * *',true,'{"quality":{}}',NOW()-INTERVAL '1 hour',$3) RETURNING id,updated_at`, a.ID, review.ModelID, review.LeaseUntil).Scan(&review.PlanID, &review.PlanUpdatedAt))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message)
 VALUES($1,'failed',$2,$3,$3,jsonb_build_object('model_id',$4::text),$5,'observed','{"verdict":"incorrect"}','answer_mismatch')`,
		review.PlanID, review.StartedAt, now, review.ModelID, review.LeaseUntil.Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, now))
	revision, err := repo.ReviewOAuthDegradationCandy(ctx, a.ID, review)
	require.NoError(t, err)
	require.Equal(t, c.Revision, revision)
	review.RateLimited, review.Revision = 2, "superseded-revision"
	_, err = repo.ReviewOAuthDegradationCandy(ctx, a.ID, review)
	require.NoError(t, err)
	before, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, before.HasOAuthDegradationAction())
	review.Revision = revision
	for range 2 {
		_, err = repo.ReviewOAuthDegradationCandy(ctx, a.ID, review)
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'
 AND details->'degradation'->>'trigger_reason'=$2 AND details->'degradation'->'candy_review' @> '{"incorrect":0,"rate_limited":2}'`, a.ID, service.OAuthDegradationCandyRetested).Scan(&count))
	require.Equal(t, 1, count)
}

func TestOAuthDegradation429PostgresWithoutProbeVerdicts(t *testing.T) {
	repo, c := degradationPolicyFixture(t)
	ctx := t.Context()
	off := false
	c.DegradationAction.Upstream429Enabled = nil
	c.DegradationAction.ProbeEnabled = &off
	c.DegradationAction.ThresholdPercent = nil
	save := func() {
		raw, err := json.Marshal(c)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, string(raw))
		require.NoError(t, err)
	}
	save()
	// Authentication failures can raise the error rate but cannot supply 429 evidence.
	for _, limited := range []int{0, 4} {
		a := degradationAccountFixture(t, 20, nil, nil, time.Minute)
		now := time.Now()
		for i := 0; i < 20; i++ {
			status := 401
			if i < limited {
				status = 429
			}
			upstreamstats.Default.Record(a.ID, status, now)
		}
		require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, now))
		got, err := repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		require.False(t, got.HasOAuthDegradationAction())
		require.Equal(t, 20, got.Concurrency)
	}

	for _, rounds := range [][]string{nil, {"unknown"}, {"correct"}} {
		a := degradationAccountFixture(t, 20, nil, rounds, time.Minute)
		now := time.Now()
		for range 19 {
			upstreamstats.Default.Record(a.ID, 429, now)
		}
		require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, now))
		got, err := repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		require.False(t, got.HasOAuthDegradationAction())
		upstreamstats.Default.Record(a.ID, 200, now)
		// The main switch still gates a default-on HTTP condition.
		c.DegradationAction.Enabled = false
		save()
		require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, now))
		got, err = repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		require.False(t, got.HasOAuthDegradationAction())
		c.DegradationAction.Enabled = true
		save()
		require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, now))
		got, err = repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		require.False(t, got.HasOAuthDegradationAction(), "HTTP evidence cannot authorize an account action")
		require.Equal(t, 20, got.Concurrency)
		evidence := upstreamstats.Default.Snapshot([]int64{a.ID}, now.Add(-time.Hour), now)[a.ID]
		require.EqualValues(t, 19, evidence.RateLimited)
		require.EqualValues(t, 20, evidence.Total)
		require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, now.Add(2*time.Hour)))
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'", a.ID).Scan(&n))
		require.Zero(t, n, "expired pressure must never become authorization")
	}
}
