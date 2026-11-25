//go:build integration

package repository

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuth429DedupCapacityNeverForgetsLiveIdentity(t *testing.T) {
	repo, a, c := oauth429Fixture(t, nil)
	ctx := t.Context()
	observation := oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, 1, 200)
	queued, err := repo.ObserveOAuth429(ctx, observation)
	require.NoError(t, err)
	require.False(t, queued)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO oauth_429_request_dedup(account_id,model,request_hash,observed_at,expires_at)
 SELECT $1,'gpt-6-astra',lpad(i::text,64,'0'),clock_timestamp(),clock_timestamp()+interval '1 hour' FROM generate_series(1,$2::integer) i`, a.ID, oauth429DedupLimit-1)
	require.NoError(t, err)
	fresh := oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, 2, 429)
	queued, err = repo.ObserveOAuth429(ctx, fresh)
	require.ErrorIs(t, err, errOAuth429DedupCapacity)
	require.False(t, queued)
	retry := observation
	retry.Status = 429
	retry.At = time.Now().UTC()
	queued, err = repo.ObserveOAuth429(ctx, retry)
	require.NoError(t, err)
	require.False(t, queued)
	var total int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM oauth_429_request_dedup WHERE account_id=$1`, a.ID).Scan(&total))
	require.Equal(t, oauth429DedupLimit, total)
	// Expired identities can be reclaimed; live entries are never evicted.
	_, err = integrationDB.ExecContext(ctx, `UPDATE oauth_429_request_dedup SET expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1 AND request_hash=lpad('1',64,'0')`, a.ID)
	require.NoError(t, err)
	queued, err = repo.ObserveOAuth429(ctx, fresh)
	require.NoError(t, err)
	require.False(t, queued)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM oauth_429_request_dedup WHERE account_id=$1`, a.ID).Scan(&total))
	require.Equal(t, oauth429DedupLimit, total)
}

func TestOAuth429AccountEpochRejectsInflightAndPriorRun(t *testing.T) {
	repo, a, c := oauth429Fixture(t, nil)
	ctx := t.Context()
	for i := 0; i < 20; i++ {
		_, err := repo.ObserveOAuth429(ctx, oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, i, 429))
		require.NoError(t, err)
	}
	oldTask, err := repo.ClaimOAuth429(ctx)
	require.NoError(t, err)
	require.NotNil(t, oldTask)
	oldRun, err := repo.StartOAuth429Run(ctx, oldTask)
	require.NoError(t, err)
	require.NotNil(t, oldRun)
	started := time.Now().UTC()
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=false WHERE id=$1`, a.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=true WHERE id=$1`, a.ID)
	require.NoError(t, err)
	// Headers arrive after resume, but the request began under the old epoch.
	delayed := oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, 20, 429)
	delayed.StartedAt = started
	queued, err := repo.ObserveOAuth429(ctx, delayed)
	require.NoError(t, err)
	require.False(t, queued)
	for i := 21; i < 41; i++ {
		q, e := repo.ObserveOAuth429(ctx, oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, i, 429))
		require.NoError(t, e)
		require.Equal(t, i == 40, q)
	}
	current, err := repo.ClaimOAuth429(ctx)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Greater(t, current.AccountEpoch, oldTask.AccountEpoch)
	require.NotEqual(t, current.TriggerID, oldTask.TriggerID)
	oldRun.AnswerComplete = true
	oldRun.Verdict = "incorrect"
	oldRun.ResultID = "late-old-result"
	oldRun.FinishedAt = time.Now().UTC()
	require.NoError(t, repo.FinishOAuth429Run(ctx, oldTask, *oldRun))
	require.NoError(t, repo.CompleteOAuth429(ctx, oldTask))
	// Even copying the old run into the current lease cannot match a reserved slot.
	require.NoError(t, repo.FinishOAuth429Run(ctx, current, *oldRun))
	var raw []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT data FROM oauth_429_tasks WHERE account_id=$1 AND model='gpt-6-astra'`, a.ID).Scan(&raw))
	var stored service.OAuth429Task
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.Empty(t, stored.Runs)
	for i := 0; i < 2; i++ {
		run, e := repo.StartOAuth429Run(ctx, current)
		require.NoError(t, e)
		require.NotNil(t, run)
		run.AnswerComplete = true
		run.Verdict = "correct"
		run.ResultID = fmt.Sprintf("fresh-result-%d", i)
		run.FinishedAt = time.Now().UTC()
		require.NoError(t, repo.FinishOAuth429Run(ctx, current, *run))
	}
	require.NoError(t, repo.CompleteOAuth429(ctx, current))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.HasOAuthDegradationAction())
}

func TestOAuth429ExpiryAdmissionAndLeaseSelection(t *testing.T) {
	repo, a, c := oauth429Fixture(t, nil)
	ctx := t.Context()
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET auto_pause_on_expired=true,expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		q, e := repo.ObserveOAuth429(ctx, oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, i, 429))
		require.NoError(t, e)
		require.False(t, q)
	}
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM oauth_429_tasks WHERE account_id=$1`, a.ID).Scan(&count))
	require.Zero(t, count)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET expires_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	for i := 20; i < 40; i++ {
		_, e := repo.ObserveOAuth429(ctx, oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, i, 429))
		require.NoError(t, e)
	}
	// A full page of valid leases cannot hide a ready task on another model.
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO oauth_429_tasks(account_id,model,state,data,updated_at)
 SELECT $1,'leased-fixture-'||i,'running',jsonb_build_object('lease_until',clock_timestamp()+interval '1 hour'),clock_timestamp()-interval '1 minute'
 FROM generate_series(1,100) i`, a.ID)
	require.NoError(t, err)
	task, err := repo.ClaimOAuth429(ctx)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, "gpt-6-astra", task.Model)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	valid, err := repo.OAuth429Current(ctx, task)
	require.NoError(t, err)
	require.False(t, valid)
	run, err := repo.StartOAuth429Run(ctx, task)
	require.NoError(t, err)
	require.Nil(t, run)
}

func TestOAuth429FirstObservationCreatesTaskBeforeDedup(t *testing.T) {
	repo, a, c := oauth429Fixture(t, nil)
	observation := oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, 1, 200)
	queued, err := repo.ObserveOAuth429(t.Context(), observation)
	require.NoError(t, err)
	require.False(t, queued)
	var tasks, identities int
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT count(*) FROM oauth_429_tasks WHERE account_id=$1`, a.ID).Scan(&tasks))
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT count(*) FROM oauth_429_request_dedup WHERE account_id=$1`, a.ID).Scan(&identities))
	require.Equal(t, 1, tasks)
	require.Equal(t, 1, identities)
}
