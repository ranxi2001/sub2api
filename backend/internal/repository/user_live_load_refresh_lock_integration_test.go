//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/regional/slots"
	"github.com/stretchr/testify/require"
)

func TestUserLiveLoadRegionalRefreshWaitsForLockBeforeCheckingExpiry(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprintf("local=%t", local), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c := userLiveLoadRegionalCache(t, local)
			r, slot := userLiveLoadUnlimitedRequest(t, c)
			defer slot.ReleaseFunc()
			ok, err := c.AcquireLiveLeaseTransferring(ctx, r)
			require.NoError(t, err)
			require.True(t, ok)
			slot.ReleaseFunc()
			// A dedicated connection identifies the actual blocked Refresh call.
			refreshDB, err := sql.Open("postgres", integrationPostgresDSN)
			require.NoError(t, err)
			defer func() { _ = refreshDB.Close() }()
			refreshDB.SetMaxOpenConns(1)
			var refreshPID int
			require.NoError(t, refreshDB.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&refreshPID))
			p := &slots.Primary{DB: refreshDB}
			var l *slots.Local
			if local {
				p = nil
				l = &slots.Local{DB: refreshDB, Region: c.region()}
			} else {
				p.Limit = c.primary.Limit
			}
			wrapped, err := NewRegionalConcurrencyCache(c.ConcurrencyCache, p, l)
			require.NoError(t, err)
			refresher, ok := wrapped.(*regionalConcurrencyCache)
			require.True(t, ok)
			_, err = c.database().ExecContext(ctx, `UPDATE regional_live_handoffs SET identity=jsonb_set(identity,
'{_user_stats_expires_at}',to_jsonb(extract(epoch FROM clock_timestamp())+1)) WHERE region_id=$1 AND lease_id=$2`, c.region(), r.LeaseID)
			require.NoError(t, err)
			before := userLiveLoadProof(t, c, r)
			locker, err := c.database().BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = locker.Rollback() }()
			var leaseID string
			require.NoError(t, locker.QueryRowContext(ctx, `SELECT lease_id FROM regional_live_handoffs
WHERE region_id=$1 AND lease_id=$2 FOR UPDATE`, c.region(), r.LeaseID).Scan(&leaseID))
			type result struct {
				held bool
				err  error
			}
			done := make(chan result, 1)
			go func() {
				held, err := refresher.RefreshLiveLease(ctx, r.AccountID, r.UserID, r.APIKeyID, r.LeaseID)
				done <- result{held: held, err: err}
			}()
			require.Eventually(t, func() bool {
				var waiting bool
				err := c.database().QueryRowContext(ctx, `SELECT wait_event_type='Lock' FROM pg_stat_activity WHERE pid=$1`, refreshPID).Scan(&waiting)
				return err == nil && waiting
			}, 500*time.Millisecond, 5*time.Millisecond, "Refresh must really wait behind the row-only lock")
			require.Eventually(t, func() bool {
				var expired bool
				err := c.database().QueryRowContext(ctx, `SELECT (identity->>'_user_stats_expires_at')::numeric<=extract(epoch FROM clock_timestamp())
FROM regional_live_handoffs WHERE region_id=$1 AND lease_id=$2`, c.region(), r.LeaseID).Scan(&expired)
				return err == nil && expired
			}, 1500*time.Millisecond, 5*time.Millisecond)
			requireUserLiveLoadUnknown(t, c, r.UserID)
			// Commit only the lock, without updating identity/xmin, within the
			// actual service's three-second operation budget.
			require.NoError(t, locker.Commit())
			got := <-done
			require.NoError(t, got.err)
			require.True(t, got.held, "auxiliary expiry cannot change admission held")
			require.Equal(t, before, userLiveLoadProof(t, c, r))
			requireUserLiveLoadUnknown(t, c, r.UserID)
		})
	}
}
