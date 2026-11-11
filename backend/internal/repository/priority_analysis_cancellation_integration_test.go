//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Only annotate the real SQL; never replace the database or query body.
type analysisMarkedSQL struct {
	sqlExecutor
	marker string
}

func (r analysisMarkedSQL) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return r.sqlExecutor.QueryContext(ctx, "/* "+r.marker+" */ "+query, args...)
}
func analysisPGActive(t *testing.T, marker string, lockOnly bool) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var count int
	query := `SELECT count(*) FROM pg_stat_activity WHERE state='active' AND position($1 in query)=1`
	if lockOnly {
		query += ` AND wait_event_type='Lock'`
	}
	require.NoError(t, integrationDB.QueryRowContext(ctx, query, "/* "+marker+" */").Scan(&count))
	return count
}
func TestPriorityAnalysisPostgresCancellationAndDeadline(t *testing.T) {
	for _, reader := range []string{"signals", "latency", "service"} {
		for _, mode := range []string{"cancel", "deadline"} {
			t.Run(reader+"/"+mode, func(t *testing.T) {
				// Reads below use independent SQL connections, so their account
				// fixture must be committed and visible outside a setup transaction.
				client := testEntClient(t)
				account := mustCreateAccount(t, client, &service.Account{Name: "analysis-cancel-fixture", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
				t.Cleanup(func() { require.NoError(t, client.Account.DeleteOneID(account.ID).Exec(context.Background())) })
				marker := fmt.Sprintf("analysis_cancel_%s_%s_%d", reader, mode, time.Now().UnixNano())
				usage := newUsageLogRepositoryWithSQL(client, analysisMarkedSQL{sqlExecutor: integrationDB, marker: marker})
				// Fixture setup touches only accounts; no uncommitted usage writes
				// can prevent this independent transaction taking the table lock.
				blocker, err := integrationDB.BeginTx(context.Background(), nil)
				require.NoError(t, err)
				defer blocker.Rollback()
				_, err = blocker.ExecContext(context.Background(), `LOCK TABLE usage_logs IN ACCESS EXCLUSIVE MODE`)
				require.NoError(t, err)
				ctx, cancel := context.WithCancel(context.Background())
				if mode == "deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
				}
				defer cancel()
				now := time.Now()
				read := func(ctx context.Context) error {
					switch reader {
					case "signals":
						_, err := usage.ReadPrioritySchedulingSignals(ctx, service.PrioritySchedulingQuery{AccountIDs: []int64{account.ID}, Model: "fixture", UsageSince: now.Add(-time.Hour), UsageUntil: now, QualitySince: now.Add(-time.Hour), LatencyPercentile: 90, LatencyMeasurement: "legacy_forward", TargetTTFTMs: 3000})
						return err
					case "latency":
						_, err := usage.ReadPriorityLatencySeries(ctx, service.PriorityLatencySeriesQuery{AccountIDs: []int64{account.ID}, Model: "fixture", Since: now.Add(-time.Hour), Until: now, BucketMinutes: 5, LatencyPercentile: 90, LatencyMeasurement: "legacy_forward"})
						return err
					default:
						_, err := service.ReadPriorityAnalysis(ctx, newAccountRepositoryWithSQL(client, integrationDB, nil), usage, service.DefaultPrioritySchedulingConfig(), service.PriorityAnalysisQuery{Model: "fixture", Minutes: 60})
						return err
					}
				}
				done := make(chan error, 1)
				go func() { done <- read(ctx) }()
				require.Eventually(t, func() bool { return analysisPGActive(t, marker, true) == 1 }, time.Second, 10*time.Millisecond, "actual PostgreSQL query must reach the controlled lock before cancellation")
				if mode == "cancel" {
					cancel()
				}
				select {
				case err := <-done:
					require.Error(t, err, "cancelled real SQL must fail")
				case <-time.After(4 * time.Second):
					t.Fatal("PostgreSQL query ignored context cancellation")
				}
				require.Error(t, ctx.Err())
				require.Eventually(t, func() bool { return analysisPGActive(t, marker, false) == 0 }, time.Second, 10*time.Millisecond, "cancelled PostgreSQL query must leave pg_stat_activity")
				require.NoError(t, blocker.Rollback())
				fresh, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				require.NoError(t, read(fresh), "fresh read must work after cancellation releases resources")
			})
		}
	}
}
