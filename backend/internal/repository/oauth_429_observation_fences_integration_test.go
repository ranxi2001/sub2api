//go:build integration

package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func oauth429ObservationFixture(id int64, model, revision string, index, status int) service.OAuth429Observation {
	digest := sha256.Sum256([]byte(fmt.Sprintf("retained-business-%d", index)))
	return service.OAuth429Observation{AccountID: id, Model: model, Revision: revision, OAuth429Sample: service.OAuth429Sample{Request: hex.EncodeToString(digest[:]), Status: status, At: time.Now().UTC()}}
}

func TestOAuth429DedupSurvivesStatisticalWindowEviction(t *testing.T) {
	repo, a, c := oauth429Fixture(t, nil)
	// 359 distinct business requests leave 159/200 failures, just below 80%.
	// The first successful response's hash is now outside the statistics ring.
	for i := 0; i < 359; i++ {
		status := 200
		if i >= 200 {
			status = 429
		}
		queued, err := repo.ObserveOAuth429(t.Context(), oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, i, status))
		require.NoError(t, err)
		require.False(t, queued)
	}
	var before []byte
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT data FROM oauth_429_tasks WHERE account_id=$1 AND model='gpt-6-astra'`, a.ID).Scan(&before))
	restarted := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	oldRetry := oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, 0, 429)
	queued, err := restarted.ObserveOAuth429(t.Context(), oldRetry)
	require.NoError(t, err)
	require.False(t, queued, "evicted business retry must not become the 160th independent failing sample")
	var after []byte
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT data FROM oauth_429_tasks WHERE account_id=$1 AND model='gpt-6-astra'`, a.ID).Scan(&after))
	require.JSONEq(t, string(before), string(after), "first response remains authoritative after restart and ring eviction")
	// Two replicas cannot reserve the same new request twice.
	fresh := oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, 359, 429)
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for _, worker := range []*accountRepository{repo, restarted} {
		wg.Add(1)
		go func(r *accountRepository) {
			defer wg.Done()
			q, e := r.ObserveOAuth429(t.Context(), fresh)
			results <- q
			errs <- e
		}(worker)
	}
	wg.Wait()
	close(results)
	close(errs)
	count := 0
	for q := range results {
		if q {
			count++
		}
	}
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, count)
	// The same request identities on another model have an independent scope.
	for i := 0; i < 20; i++ {
		q, e := repo.ObserveOAuth429(t.Context(), oauth429ObservationFixture(a.ID, "gpt-6-luna", c.Revision, i, 429))
		require.NoError(t, e)
		require.Equal(t, i == 19, q)
	}
	other := degradationAccountFixture(t, 50, nil, nil, time.Minute)
	for i := 0; i < 20; i++ {
		q, e := repo.ObserveOAuth429(t.Context(), oauth429ObservationFixture(other.ID, "gpt-6-astra", c.Revision, i, 429))
		require.NoError(t, e)
		require.Equal(t, i == 19, q)
	}
}

func TestOAuth429QueuedObservationCannotCrossAccountTransition(t *testing.T) {
	for _, transition := range []string{"stop_resume", "delete_restore", "model_change_restore", "expiry_change"} {
		t.Run(transition, func(t *testing.T) {
			repo, a, c := oauth429Fixture(t, nil)
			for i := 0; i < 19; i++ {
				queued, err := repo.ObserveOAuth429(t.Context(), oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, i, 429))
				require.NoError(t, err)
				require.False(t, queued)
			}
			// This immutable observation is held at the async queue/DB boundary.
			delayed := oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, 19, 429)
			var statement string
			switch transition {
			case "stop_resume":
				statement = `UPDATE accounts SET schedulable=false WHERE id=$1;`
			case "delete_restore":
				statement = `UPDATE accounts SET deleted_at=clock_timestamp() WHERE id=$1;`
			case "model_change_restore":
				statement = `UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}','{"gpt-6-astra":"gpt-6-luna"}') WHERE id=$1;`
			case "expiry_change":
				statement = `UPDATE accounts SET auto_pause_on_expired=true,expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1;`
			}
			_, err := integrationDB.ExecContext(t.Context(), statement, a.ID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(t.Context(), `UPDATE accounts SET schedulable=true,deleted_at=NULL,expires_at=NULL,credentials=jsonb_set(credentials,'{model_mapping}','{"gpt-6-astra":"gpt-6-astra","gpt-6-luna":"gpt-6-luna"}') WHERE id=$1`, a.ID)
			require.NoError(t, err)
			restarted := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
			queued, err := restarted.ObserveOAuth429(t.Context(), delayed)
			require.NoError(t, err)
			require.False(t, queued, "queued old-epoch sample must be rejected even after the account becomes schedulable again")
			var raw []byte
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT data FROM oauth_429_tasks WHERE account_id=$1 AND model='gpt-6-astra'`, a.ID).Scan(&raw))
			var task service.OAuth429Task
			require.NoError(t, json.Unmarshal(raw, &task))
			require.Empty(t, task.Samples, "observing samples must also be invalidated by the account transition")
			for i := 20; i < 40; i++ {
				q, e := restarted.ObserveOAuth429(t.Context(), oauth429ObservationFixture(a.ID, "gpt-6-astra", c.Revision, i, 429))
				require.NoError(t, e)
				require.Equal(t, i == 39, q, "only the new account epoch supplies the immediate threshold")
			}
		})
	}
}
