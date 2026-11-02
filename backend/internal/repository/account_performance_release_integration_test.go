//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/regional/slots"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func performanceCommittedAccount(t *testing.T) int64 {
	t.Helper()
	a := mustCreateAccount(t, integrationEntClient, &service.Account{Name: fmt.Sprintf("performance-race-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, Concurrency: 4})
	t.Cleanup(func() {
		_, err := integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`, a.ID)
		require.NoError(t, err)
		_, err = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, a.ID)
		require.NoError(t, err)
	})
	return a.ID
}

func TestAccountPerformancePostgresSoftDeleteWinsAgainstMutations(t *testing.T) {
	for _, action := range []string{"save", "evaluate", "release"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			id := performanceCommittedAccount(t)
			_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_performance_monitors(account_id,config,paused,revision,pause_generation) VALUES($1,'{"enabled":true}', $2,1,1)`, id, action == "release")
			require.NoError(t, err)
			deletion, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = deletion.Rollback() }()
			_, err = deletion.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, id)
			require.NoError(t, err)
			repo := &accountPerformanceRepository{db: integrationDB}
			done := make(chan error, 1)
			go func() {
				switch action {
				case "save":
					done <- repo.Save(ctx, id, service.PerformanceConfig{Enabled: true}, 1, 0)
				case "evaluate":
					done <- repo.Evaluate(ctx, id, time.Now())
				case "release":
					done <- repo.Release(ctx, id, 1, 1, 0)
				}
			}()
			var result error
			early := false
			select {
			case result = <-done:
				early = true
				t.Error("monitor mutation bypassed the in-flight soft deletion")
			case <-time.After(100 * time.Millisecond):
			}
			require.NoError(t, deletion.Commit())
			if !early {
				result = <-done
			}
			if action == "evaluate" {
				require.NoError(t, result, "deleted evaluator candidates are skipped")
			} else {
				require.ErrorIs(t, result, service.ErrPerformanceConflict)
			}
			var count int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_performance_events WHERE account_id=$1`, id).Scan(&count))
			require.Zero(t, count, "deleted accounts must not receive new monitor audit events")
			metrics, err := repo.Read(ctx, []int64{id}, time.Now())
			require.NoError(t, err)
			require.Empty(t, metrics, "batch reads must not expose soft-deleted accounts")
		})
	}
}

func TestAccountPerformancePostgresAccountLockBlocksSoftDelete(t *testing.T) {
	ctx := context.Background()
	id := performanceCommittedAccount(t)
	monitor, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = monitor.Rollback() }()
	require.NoError(t, lockPerformanceAccount(ctx, monitor, id))
	deletion, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = deletion.Rollback() }()
	_, err = deletion.ExecContext(ctx, `SET LOCAL lock_timeout='100ms'`)
	require.NoError(t, err)
	_, err = deletion.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, id)
	require.ErrorContains(t, err, "lock timeout", "non-key soft deletion must wait until the monitor transaction ends")
}

func TestAccountPerformancePostgresRegionalSlotsRejectPausedAccount(t *testing.T) {
	ctx := context.Background()
	id := performanceCommittedAccount(t)
	tx, err := integrationDB.BeginTx(ctx, &sql.TxOptions{})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	primary := NewRegionalSlotAuthority(integrationDB)
	resource := slots.Resource{Kind: "account", ID: id}
	limit, err := primary.Limit(ctx, tx, resource)
	require.NoError(t, err)
	require.Equal(t, 4, limit)
	_, err = tx.ExecContext(ctx, `INSERT INTO account_performance_monitors(account_id,paused) VALUES($1,true)`, id)
	require.NoError(t, err)
	limit, err = primary.Limit(ctx, tx, resource)
	require.NoError(t, err)
	require.Zero(t, limit, "new grants, renewals and primary acquisitions must deny paused accounts")
}
