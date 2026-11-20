//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type confirmedProbeFixture struct {
	review        service.OAuthDegradationCandyReview
	ownsLease     bool
	previousLease sql.NullTime
}

var confirmedProbeFixtures sync.Map

// Legacy action tests enter the existing probe/candy confirmation API. Reuse
// the account's quality plan and live lease: a second quarantine plan violates
// the production scope constraint and must not be fabricated by the fixture.
func prepareConfirmedProbeFixture(t *testing.T, id int64) {
	t.Helper()
	if _, ok := confirmedProbeFixtures.Load(id); ok {
		return
	}
	ctx := t.Context()
	var raw string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, service.SettingKeyOAuthAutoConfig).Scan(&raw))
	var cfg service.OAuthAutoConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &cfg))
	now := time.Now().UTC().Truncate(time.Microsecond)
	f := confirmedProbeFixture{review: service.OAuthDegradationCandyReview{StartedAt: now, Incorrect: 2, Revision: cfg.Revision}}
	err := integrationDB.QueryRowContext(ctx, `SELECT id,model_id,updated_at,running_until FROM scheduled_test_plans
 WHERE account_id=$1 AND pelican_config->'quality' IS NOT NULL
 ORDER BY enabled DESC,CASE WHEN COALESCE(pelican_config->'quality'->>'action','') NOT IN ('enable_bps','observe_only') THEN 0 ELSE 1 END,id LIMIT 1`, id).Scan(&f.review.PlanID, &f.review.ModelID, &f.review.PlanUpdatedAt, &f.previousLease)
	created := errors.Is(err, sql.ErrNoRows)
	if created {
		f.review.ModelID = "gpt-6-luna"
		err = integrationDB.QueryRowContext(ctx, `INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config,updated_at)
 VALUES($1,$2,'*/30 * * * *',true,'{"quality":{}}',$3) RETURNING id,updated_at`, id, f.review.ModelID, now.Add(-time.Minute)).Scan(&f.review.PlanID, &f.review.PlanUpdatedAt)
	}
	require.NoError(t, err)
	f.ownsLease = !f.previousLease.Valid || !f.previousLease.Time.After(now)
	f.review.LeaseUntil = f.previousLease.Time
	if f.ownsLease {
		f.review.LeaseUntil = now.Add(10 * time.Minute)
		_, err = integrationDB.ExecContext(ctx, `UPDATE scheduled_test_plans SET running_until=$2 WHERE id=$1`, f.review.PlanID, f.review.LeaseUntil)
		require.NoError(t, err)
	}
	// The incomplete confirmation fixture does not add a performance sample.
	// Existing tests keep their exact denominator and action/rollback assertions.
	var resultID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message)
 VALUES($1,'failed',$2,$2,$2,jsonb_build_object('model_id',$3::text),$4,'comparison_pending','{"verdict":"incorrect"}','answer_mismatch') RETURNING id`, f.review.PlanID, now, f.review.ModelID, f.review.LeaseUntil.Format(time.RFC3339Nano)).Scan(&resultID))
	confirmedProbeFixtures.Store(id, f)
	t.Cleanup(func() {
		confirmedProbeFixtures.Delete(id)
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM scheduled_test_results WHERE id=$1`, resultID)
		require.NoError(t, err)
		if created {
			_, err = integrationDB.ExecContext(context.Background(), `DELETE FROM scheduled_test_plans WHERE id=$1`, f.review.PlanID)
		} else if f.ownsLease {
			err = f.restoreLease(context.Background(), integrationDB)
		}
		require.NoError(t, err)
	})
}
func (f confirmedProbeFixture) restoreLease(ctx context.Context, db sqlExecutor) error {
	_, err := db.ExecContext(ctx, `UPDATE scheduled_test_plans SET running_until=$2 WHERE id=$1 AND running_until=$3 AND updated_at=$4`, f.review.PlanID, f.previousLease, f.review.LeaseUntil, f.review.PlanUpdatedAt)
	return err
}
func evaluateConfirmedProbe(ctx context.Context, repo *accountRepository, id int64, now time.Time) (err error) {
	value, ok := confirmedProbeFixtures.Load(id)
	if !ok {
		return repo.EvaluateOAuthDegradation(ctx, id, now)
	}
	f := value.(confirmedProbeFixture)
	if f.ownsLease {
		// Avoid taking an exclusive plan lock when replicas already share the fixture
		// lease; deterministic tests must both reach the production account barrier.
		db, dbErr := repo.oauth429DB()
		if dbErr != nil {
			return dbErr
		}
		var current sql.NullTime
		if err = db.QueryRowContext(ctx, `SELECT running_until FROM scheduled_test_plans WHERE id=$1`, f.review.PlanID).Scan(&current); err != nil {
			return err
		}
		if current == f.previousLease {
			if _, err = db.ExecContext(ctx, `UPDATE scheduled_test_plans SET running_until=$2 WHERE id=$1 AND running_until IS NOT DISTINCT FROM $3 AND updated_at=$4`, f.review.PlanID, f.review.LeaseUntil, f.previousLease, f.review.PlanUpdatedAt); err != nil {
				return err
			}
		}
		defer func() {
			if restoreErr := f.restoreLease(ctx, db); err == nil {
				err = restoreErr
			}
		}()
	}
	return repo.evaluateOAuthDegradation(ctx, id, now, &f.review)
}

type confirmedProbeAccounts struct{ *accountRepository }

func (r confirmedProbeAccounts) EvaluateOAuthDegradation(ctx context.Context, id int64, now time.Time) error {
	return evaluateConfirmedProbe(ctx, r.accountRepository, id, now)
}

func TestOAuthDegradationProbeFixtureReusesScopeAndLease(t *testing.T) {
	repo, _ := degradationPolicyFixture(t)
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: service.QualityActionRemoveModel, RemoveModels: []string{"gpt-6-astra"}})
	var before int
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT count(*) FROM scheduled_test_plans WHERE account_id=$1`, f.account).Scan(&before))
	prepareConfirmedProbeFixture(t, f.account)
	value, ok := confirmedProbeFixtures.Load(f.account)
	require.True(t, ok)
	fixture := value.(confirmedProbeFixture)
	require.Equal(t, f.plan.ID, fixture.review.PlanID)
	require.Equal(t, f.plan.ModelID, fixture.review.ModelID)
	require.True(t, f.until.Equal(fixture.review.LeaseUntil))
	require.False(t, fixture.ownsLease, "do not replace another fixture's claimed lease")
	require.NoError(t, evaluateConfirmedProbe(t.Context(), repo, f.account, time.Now()))
	var after int
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT count(*) FROM scheduled_test_plans WHERE account_id=$1`, f.account).Scan(&after))
	require.Equal(t, before, after)
	var until time.Time
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT running_until FROM scheduled_test_plans WHERE id=$1`, f.plan.ID).Scan(&until))
	require.True(t, f.until.Equal(until))
}
