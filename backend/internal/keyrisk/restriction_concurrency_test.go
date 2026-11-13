package keyrisk

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Observe a real PostgreSQL lock wait before releasing the competing writer.
// Timing alone cannot establish that the restriction read the older snapshot.
func waitForRestrictionLock(t *testing.T, ctx context.Context, db *sql.DB, blocker int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE $1=ANY(pg_blocking_pids(pid)) AND wait_event_type='Lock'
 AND (query LIKE 'SELECT k.id,k.user_id FROM key_risk_events%'
 OR query LIKE 'UPDATE api_keys SET ip_whitelist=%'))`, blocker).Scan(&waiting)
		require.NoError(t, err)
		return waiting
	}, 3*time.Second, 10*time.Millisecond, "restriction must reach the controlled database lock")
}

func TestRestrictionRejectsConcurrentEpisodeOrKeyChanges(t *testing.T) {
	for _, change := range []string{"reopened", "resolved", "ignored", "owner", "disabled", "deleted", "expired", "blacklist"} {
		t.Run(change, func(t *testing.T) {
			s, _ := fixture(t)
			s.DB.SetMaxOpenConns(3) // competing writer, restriction, lock observer
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			run(t, s)
			events, err := s.Events(ctx, 0)
			require.NoError(t, err)
			require.Len(t, events, 1)
			ev := events[0]
			blocker, err := s.DB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() {
				if err := blocker.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
					t.Errorf("rollback competing writer: %v", err)
				}
			}()
			var pid int
			require.NoError(t, blocker.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
			// Hold both rows first, avoiding dependence on the join's lock order.
			var keyID int64
			require.NoError(t, blocker.QueryRowContext(ctx, `SELECT k.id FROM key_risk_events e
 JOIN api_keys k ON e.subject=k.id::text WHERE e.id=$1 FOR UPDATE OF e,k`, ev.ID).Scan(&keyID))
			type result struct {
				key string
				err error
			}
			done := make(chan result, 1)
			go func() {
				key, err := s.RestrictIPWhitelist(ctx, ev.ID, ev.Generation, []string{"203.0.113.0/24"})
				done <- result{key, err}
			}()
			waitForRestrictionLock(t, ctx, s.DB, pid)
			switch change {
			case "reopened":
				_, err = blocker.ExecContext(ctx, "UPDATE key_risk_events SET state='resolved' WHERE id=$1", ev.ID)
				require.NoError(t, err)
				_, err = blocker.ExecContext(ctx, "UPDATE key_risk_events SET state='pending',generation=generation+1 WHERE id=$1", ev.ID)
			case "resolved", "ignored":
				_, err = blocker.ExecContext(ctx, "UPDATE key_risk_events SET state=$1 WHERE id=$2", change, ev.ID)
			case "owner":
				_, err = blocker.ExecContext(ctx, "UPDATE api_keys SET user_id=2 WHERE id=11")
			case "disabled":
				_, err = blocker.ExecContext(ctx, "UPDATE api_keys SET status='disabled' WHERE id=11")
			case "deleted":
				_, err = blocker.ExecContext(ctx, "UPDATE api_keys SET deleted_at=now() WHERE id=11")
			case "expired":
				_, err = blocker.ExecContext(ctx, "UPDATE api_keys SET expires_at=clock_timestamp()-interval '1 second' WHERE id=11")
			case "blacklist":
				_, err = blocker.ExecContext(ctx, `UPDATE api_keys SET ip_blacklist='["203.0.113.8"]' WHERE id=11`)
			}
			require.NoError(t, err)
			_, err = blocker.ExecContext(ctx, `UPDATE api_keys SET ip_whitelist='["198.51.100.0/24"]' WHERE id=11`)
			require.NoError(t, err)
			require.NoError(t, blocker.Commit())
			select {
			case got := <-done:
				if change == "blacklist" {
					require.NoError(t, got.err)
					require.Equal(t, "sk-keyrisk-fixture", got.key)
				} else {
					require.ErrorIs(t, got.err, sql.ErrNoRows, "stale restriction must reject changed episode or key")
					require.Empty(t, got.key)
				}
			case <-ctx.Done():
				t.Fatal("restriction did not finish after the competing writer committed")
			}
			var allow, deny string
			require.NoError(t, s.DB.QueryRowContext(ctx, `SELECT ip_whitelist::text,COALESCE(ip_blacklist,'[]'::jsonb)::text FROM api_keys WHERE id=11`).Scan(&allow, &deny))
			if change == "blacklist" {
				require.JSONEq(t, `["203.0.113.0/24"]`, allow)
				require.JSONEq(t, `["203.0.113.8"]`, deny)
			} else {
				require.JSONEq(t, `["198.51.100.0/24"]`, allow, "stale confirmation must not overwrite the newer ACL")
			}
		})
	}
}
