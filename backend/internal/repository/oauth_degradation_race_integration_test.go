//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Each replica has an independent connection pool and ent client. The observer
// identifies real backend PIDs, not just goroutines that may not have run yet.
func degradationReplica(t *testing.T) (*accountRepository, *settingRepository, int) {
	t.Helper()
	u, err := url.Parse(integrationPostgresDSN)
	require.NoError(t, err)
	q := u.Query()
	q.Set("application_name", fmt.Sprintf("degradation-replica-%d", time.Now().UnixNano()))
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	var pid int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT pg_backend_pid()").Scan(&pid))
	return newAccountRepositoryWithSQL(client, db, NewSchedulerCache(testRedis(t))), NewSettingRepository(client).(*settingRepository), pid
}

func degradationEntered(t *testing.T, pid int, query string, blocker int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var entered bool
		err := integrationDB.QueryRowContext(t.Context(), `SELECT EXISTS(
			SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND state='active'
			AND wait_event_type='Lock' AND query LIKE $2
			AND cardinality(pg_blocking_pids(pid))>0
			AND ($3=0 OR $3=ANY(pg_blocking_pids(pid))))`, pid, query, blocker).Scan(&entered)
		return err == nil && entered
	}, 5*time.Second, 10*time.Millisecond, "real PostgreSQL entered barrier: pid=%d query=%s blocker=%d", pid, query, blocker)
	t.Logf("entered: PostgreSQL pid=%d blocked at %s (expected blocker=%d)", pid, query, blocker)
}

func degradationGatePID(t *testing.T, tx *sql.Tx) int {
	t.Helper()
	var pid int
	require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT pg_backend_pid()").Scan(&pid))
	return pid
}

func degradationRows(t *testing.T, id int64) (int, int) {
	t.Helper()
	var history, outbox int
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'", id).Scan(&history))
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1", id).Scan(&outbox))
	return history, outbox
}

func TestOAuthDegradationDeterministicReplicas(t *testing.T) {
	_, _ = degradationPolicyFixture(t)
	a := degradationAccountFixture(t, 20, nil, []string{"incorrect"}, time.Minute)
	one, _, pidOne := degradationReplica(t)
	two, _, pidTwo := degradationReplica(t)
	require.NotEqual(t, pidOne, pidTwo)
	tx := testTx(t)
	gatePID := degradationGatePID(t, tx)
	_, err := tx.ExecContext(t.Context(), "SELECT id FROM accounts WHERE id=$1 FOR UPDATE", a.ID)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- one.EvaluateOAuthDegradation(ctx, a.ID, time.Now()) }()
	degradationEntered(t, pidOne, "%FROM accounts%FOR UPDATE%", gatePID)
	go func() { results <- two.EvaluateOAuthDegradation(ctx, a.ID, time.Now()) }()
	// A queued tuple lock can make worker two wait on worker one instead of the
	// gate transaction. Both still entered the exact account-lock statement.
	degradationEntered(t, pidTwo, "%FROM accounts%FOR UPDATE%", 0)
	t.Logf("release: account=%d gate_pid=%d after both replicas entered", a.ID, gatePID)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	got, err := one.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 10, got.Concurrency)
	require.True(t, got.HasOAuthDegradationAction())
	require.Contains(t, got.Extra, service.AutoConfigConcurrencyExtraKey)
	history, outbox := degradationRows(t, a.ID)
	require.Equal(t, 1, history)
	require.Equal(t, 1, outbox, "two actual transactions must enqueue one penalty")
	restarted, _, _ := degradationReplica(t)
	require.NoError(t, restarted.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
	history, outbox = degradationRows(t, a.ID)
	require.Equal(t, 1, history)
	require.Equal(t, 1, outbox)
}

func TestOAuthDegradationDeterministicLiveFence(t *testing.T) {
	t.Run("disable_wins", func(t *testing.T) {
		_, c := degradationPolicyFixture(t)
		a := degradationAccountFixture(t, 20, nil, []string{"incorrect"}, time.Minute)
		repo, _, pid := degradationReplica(t)
		tx := testTx(t)
		gatePID := degradationGatePID(t, tx)
		_, err := tx.ExecContext(t.Context(), "SELECT value FROM settings WHERE key=$1 FOR UPDATE", service.SettingKeyOAuthAutoConfig)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()) }()
		degradationEntered(t, pid, "%settings%FOR SHARE%", gatePID)
		c.DegradationAction.Enabled = false
		raw, err := json.Marshal(c)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, string(raw))
		require.NoError(t, err)
		t.Log("release: commit live disabled policy before waiting evaluator can read it")
		require.NoError(t, tx.Commit())
		require.NoError(t, <-result)
		got, err := repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		require.Equal(t, 20, got.Concurrency)
		require.False(t, got.HasOAuthDegradationAction())
		history, outbox := degradationRows(t, a.ID)
		require.Zero(t, history)
		require.Zero(t, outbox)
	})
	t.Run("action_wins", func(t *testing.T) {
		_, c := degradationPolicyFixture(t)
		a := degradationAccountFixture(t, 20, nil, []string{"incorrect"}, time.Minute)
		repo, _, pid := degradationReplica(t)
		_, settings, saverPID := degradationReplica(t)
		tx := testTx(t)
		_, err := tx.ExecContext(t.Context(), "SELECT id FROM accounts WHERE id=$1 FOR UPDATE", a.ID)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		action, saved := make(chan error, 1), make(chan error, 1)
		go func() { action <- repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()) }()
		degradationEntered(t, pid, "%FROM accounts%FOR UPDATE%", degradationGatePID(t, tx))
		next := c
		next.Revision += "-disabled"
		next.DegradationAction.Enabled = false
		raw, err := json.Marshal(next)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, cleanupErr := integrationDB.ExecContext(context.Background(), "DELETE FROM account_auto_config_events WHERE kind='config_saved' AND details->'config'->>'revision'=$1", next.Revision)
			require.NoError(t, cleanupErr)
		})
		go func() { saved <- settings.CompareAndSetOAuthAutoConfig(ctx, string(raw), c.Revision) }()
		degradationEntered(t, saverPID, "%settings%FOR UPDATE%", pid)
		t.Log("release: evaluator holds settings SHARE; real CAS disable waits until action commits")
		require.NoError(t, tx.Commit())
		require.NoError(t, <-action)
		require.NoError(t, <-saved)
		history, outbox := degradationRows(t, a.ID)
		require.Equal(t, 1, history)
		require.Equal(t, 1, outbox)
		got, err := repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		require.Equal(t, 10, got.Concurrency)
		var current string
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", service.SettingKeyOAuthAutoConfig).Scan(&current))
		var policy service.OAuthAutoConfig
		require.NoError(t, json.Unmarshal([]byte(current), &policy))
		require.False(t, policy.DegradationAction.Enabled)
	})
}

func TestOAuthDegradationDeterministicSettingsCAS(t *testing.T) {
	_, c := degradationPolicyFixture(t)
	_, one, pidOne := degradationReplica(t)
	_, two, pidTwo := degradationReplica(t)
	tx := testTx(t)
	gatePID := degradationGatePID(t, tx)
	_, err := tx.ExecContext(t.Context(), "SELECT pg_advisory_xact_lock(681705431)")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	errs := make(chan error, 2)
	for index, settings := range []*settingRepository{one, two} {
		next := c
		next.Revision += fmt.Sprintf("-cas-%d", index)
		raw, err := json.Marshal(next)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := integrationDB.ExecContext(context.Background(), "DELETE FROM account_auto_config_events WHERE kind='config_saved' AND details->'config'->>'revision'=$1", next.Revision)
			require.NoError(t, err)
		})
		go func() { errs <- settings.CompareAndSetOAuthAutoConfig(ctx, string(raw), c.Revision) }()
	}
	degradationEntered(t, pidOne, "%pg_advisory_xact_lock%", gatePID)
	degradationEntered(t, pidTwo, "%pg_advisory_xact_lock%", 0)
	t.Log("release: both independent CAS writers have entered PostgreSQL advisory-lock wait")
	require.NoError(t, tx.Commit())
	success, conflict := 0, 0
	for range 2 {
		err := <-errs
		if err == nil {
			success++
		} else {
			require.Contains(t, err.Error(), "reload before saving")
			conflict++
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, conflict)
}

func TestOAuthDegradationTransactionFailures(t *testing.T) {
	for _, table := range []string{"scheduler_outbox", "account_auto_config_events"} {
		t.Run(table, func(t *testing.T) {
			repo, _ := degradationPolicyFixture(t)
			// Use a nonnil real cache publisher even on rollback paths.
			repo.schedulerCache = NewSchedulerCache(testRedis(t))
			a := degradationAccountFixture(t, 20, nil, []string{"incorrect"}, time.Minute)
			before, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			fn := fmt.Sprintf("degradation_fail_%d", a.ID)
			trigger := "degradation_fault"
			_, err = integrationDB.ExecContext(t.Context(), fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.account_id=%d THEN RAISE EXCEPTION 'degradation fixture transaction fault'; END IF; RETURN NEW; END $$`, fn, a.ID))
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP FUNCTION IF EXISTS %s() CASCADE", fn))
				require.NoError(t, err)
			})
			_, err = integrationDB.ExecContext(t.Context(), fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION %s()", trigger, table, fn))
			require.NoError(t, err)
			err = repo.EvaluateOAuthDegradation(t.Context(), a.ID, time.Now())
			require.ErrorContains(t, err, "degradation fixture transaction fault")
			after, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			require.Equal(t, before.Concurrency, after.Concurrency)
			require.Equal(t, before.Credentials, after.Credentials)
			require.Equal(t, before.Extra, after.Extra, "marker, hold and model penalty must roll back atomically")
			require.Equal(t, before.UpdatedAt, after.UpdatedAt)
			history, outbox := degradationRows(t, a.ID)
			require.Zero(t, history)
			require.Zero(t, outbox, "history failure must also roll back the earlier outbox insert")
			t.Logf("real PostgreSQL %s INSERT fault: concurrency/marker/hold/models/history/outbox all rolled back", table)
			_, err = integrationDB.ExecContext(t.Context(), fmt.Sprintf("DROP TRIGGER %s ON %s", trigger, table))
			require.NoError(t, err)
			require.NoError(t, repo.EvaluateOAuthDegradation(t.Context(), a.ID, time.Now()))
			history, outbox = degradationRows(t, a.ID)
			require.Equal(t, 1, history)
			require.Equal(t, 1, outbox)
		})
	}
}
