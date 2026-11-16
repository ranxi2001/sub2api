//go:build integration

package accounttimer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func dispositionFixture(t *testing.T) (*Store, *time.Time) {
	s, now, _ := integrationStore(t)
	_, err := s.DB.Exec(`ALTER TABLE accounts ADD COLUMN type text DEFAULT 'apikey', ADD COLUMN platform text DEFAULT 'openai', ADD COLUMN parent_account_id bigint;
 CREATE TABLE groups(id bigint PRIMARY KEY,platform text DEFAULT 'openai',status text DEFAULT 'active',deleted_at timestamptz);
 CREATE TABLE account_groups(account_id bigint,group_id bigint);
 INSERT INTO groups(id) VALUES(5),(6);
 INSERT INTO account_groups VALUES(7,5),(7,6);
 UPDATE accounts SET schedulable=true;`)
	require.NoError(t, err)
	return s, now
}
func dispositionPolicy() OAuthDispositionConfig {
	return OAuthDispositionConfig{Enabled: true, Scope: "groups", GroupIDs: []int64{5, 6}, Action: "pause", DelayMinutes: 5, PauseMinutes: 180, Timezone: "Asia/Shanghai"}
}
func scheduleDisposition(t *testing.T, s *Store, now time.Time, p OAuthDispositionConfig, commit bool) {
	t.Helper()
	tx, err := s.DB.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, ScheduleOAuthDisposition(t.Context(), tx, 100, "openai", p, now))
	if commit {
		require.NoError(t, tx.Commit())
	} else {
		require.NoError(t, tx.Rollback())
	}
}
func TestOAuthDispositionDurabilityAndOwnership(t *testing.T) {
	s, now := dispositionFixture(t)
	p := dispositionPolicy()
	scheduleDisposition(t, s, *now, p, false)
	snap, err := s.List(t.Context(), 7)
	require.NoError(t, err)
	require.Empty(t, snap.Tasks)
	scheduleDisposition(t, s, *now, p, true)
	snap, err = s.List(t.Context(), 7)
	require.NoError(t, err)
	require.Len(t, snap.Tasks, 2)
	require.Equal(t, "close", snap.Tasks[0].Action)
	require.Equal(t, "Asia/Shanghai", snap.Tasks[0].Timezone)
	require.Equal(t, "once", snap.Tasks[0].Recurrence)
	firstDue := snap.Tasks[0].ExecuteAt
	scheduleDisposition(t, s, now.Add(time.Minute), p, true)
	snap, err = s.List(t.Context(), 7)
	require.NoError(t, err)
	require.Equal(t, firstDue, snap.Tasks[0].ExecuteAt)
	*now = now.Add(8 * time.Minute) // Worker restarted three minutes after the deadline.
	restarted := *s
	require.NoError(t, restarted.RunDue(t.Context()))
	var schedulable bool
	require.NoError(t, s.DB.QueryRow("SELECT schedulable FROM accounts WHERE id=7").Scan(&schedulable))
	require.False(t, schedulable)
	snap, err = s.List(t.Context(), 7)
	require.NoError(t, err)
	require.Equal(t, "executed", snap.Tasks[0].Status)
	require.Equal(t, now.Add(180*time.Minute), snap.Tasks[1].ExecuteAt)
	*now = now.Add(180 * time.Minute)
	require.NoError(t, restarted.RunDue(t.Context()))
	require.NoError(t, s.DB.QueryRow("SELECT schedulable FROM accounts WHERE id=7").Scan(&schedulable))
	require.True(t, schedulable)
}
func TestOAuthDispositionEligibilityAndExistingTimers(t *testing.T) {
	for _, state := range []string{"disabled", "oauth", "platform", "shadow", "inactive", "expired", "existing"} {
		t.Run(state, func(t *testing.T) {
			s, now := dispositionFixture(t)
			mutations := map[string]string{"disabled": "schedulable=false", "oauth": "type='oauth'", "platform": "platform='anthropic'", "shadow": "parent_account_id=99", "inactive": "status='error'", "expired": "expires_at='2020-01-01'"}
			if update := mutations[state]; update != "" {
				_, err := s.DB.Exec("UPDATE accounts SET " + update + " WHERE id=7")
				require.NoError(t, err)
			}
			if state == "existing" {
				_, err := s.Save(t.Context(), 7, 1, SaveRequest{Action: "close", Input: Input{Mode: "relative", Minutes: 1, Timezone: "UTC"}})
				require.NoError(t, err)
			}
			scheduleDisposition(t, s, *now, dispositionPolicy(), true)
			snap, err := s.List(t.Context(), 7)
			require.NoError(t, err)
			if state == "existing" {
				require.Len(t, snap.Tasks, 1)
				require.Equal(t, int64(1), snap.Tasks[0].RequestedBy)
			} else {
				require.Empty(t, snap.Tasks)
			}
		})
	}
}
func TestOAuthDispositionManualAndConcurrentChanges(t *testing.T) {
	for _, change := range []string{"manual", "other_write", "no_close", "type_changed", "platform_changed", "cancel_close"} {
		t.Run(change, func(t *testing.T) {
			s, now := dispositionFixture(t)
			p := dispositionPolicy()
			p.DelayMinutes = 0
			scheduleDisposition(t, s, *now, p, true)
			if change == "platform_changed" {
				_, err := s.DB.Exec("UPDATE accounts SET platform='anthropic' WHERE id=7")
				require.NoError(t, err)
			}
			if change == "type_changed" {
				_, err := s.DB.Exec("UPDATE accounts SET type='oauth' WHERE id=7")
				require.NoError(t, err)
			}
			if change == "cancel_close" {
				require.NoError(t, s.Cancel(t.Context(), 7, 1, "close", 1))
			}
			if change != "no_close" {
				require.NoError(t, s.RunDue(t.Context()))
			}
			switch change {
			case "manual":
				require.NoError(t, s.Manual(t.Context(), 7, 1, false))
			case "other_write":
				_, err := s.DB.Exec("UPDATE accounts SET updated_at=updated_at+interval '1 second' WHERE id=7")
				require.NoError(t, err)
			case "no_close":
				_, err := s.DB.Exec("UPDATE account_schedulable_timers SET status='failed' WHERE account_id=7 AND action='close'")
				require.NoError(t, err)
			}
			*now = now.Add(180 * time.Minute)
			require.NoError(t, s.RunDue(t.Context()))
			snap, err := s.List(t.Context(), 7)
			require.NoError(t, err)
			require.Contains(t, []string{"skipped", "cancelled"}, snap.Tasks[1].Status)
		})
	}
}
func TestOAuthDispositionManualScopeDisableAndConcurrentImports(t *testing.T) {
	s, now := dispositionFixture(t)
	p := dispositionPolicy()
	p.Scope = "accounts"
	p.AccountIDs = []int64{7}
	p.Action = "disable"
	p.DelayMinutes = 0
	errors := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func(i int) {
			tx, err := s.DB.BeginTx(context.Background(), nil)
			if err != nil {
				errors <- err
				return
			}
			defer func() { _ = tx.Rollback() }()
			err = ScheduleOAuthDisposition(context.Background(), tx, int64(100+i), "openai", p, *now)
			if err == nil {
				err = tx.Commit()
			}
			errors <- err
		}(i)
	}
	for range 4 {
		require.NoError(t, <-errors)
	}
	snap, err := s.List(t.Context(), 7)
	require.NoError(t, err)
	require.Len(t, snap.Tasks, 1)
	require.Equal(t, int64(1), snap.Tasks[0].Version)
	require.NoError(t, s.RunDue(t.Context()))
	var enabled bool
	require.NoError(t, s.DB.QueryRow("SELECT schedulable FROM accounts WHERE id=7").Scan(&enabled))
	require.False(t, enabled)
	*now = now.Add(24 * time.Hour)
	require.NoError(t, s.RunDue(t.Context()))
	require.NoError(t, s.DB.QueryRow("SELECT schedulable FROM accounts WHERE id=7").Scan(&enabled))
	require.False(t, enabled, fmt.Sprint(snap))
}

func TestOAuthDispositionRetryPreservesOwnership(t *testing.T) {
	s, now := dispositionFixture(t)
	p := dispositionPolicy()
	p.DelayMinutes = 0
	scheduleDisposition(t, s, *now, p, true)
	enqueue := s.Enqueue
	s.Enqueue = func(context.Context, *sql.Tx, int64) error { return errors.New("fixture outbox unavailable") }
	require.NoError(t, s.RunDue(t.Context()))
	snap, err := s.List(t.Context(), 7)
	require.NoError(t, err)
	require.Equal(t, 1, snap.Tasks[0].Attempts)
	require.Equal(t, "oauth_initial:100:openai", snap.Tasks[0].Reason)
	s.Enqueue = enqueue
	*now = now.Add(time.Minute)
	require.NoError(t, s.RunDue(t.Context()))
	*now = now.Add(180 * time.Minute)
	require.NoError(t, s.RunDue(t.Context()))
	var enabled bool
	require.NoError(t, s.DB.QueryRow("SELECT schedulable FROM accounts WHERE id=7").Scan(&enabled))
	require.True(t, enabled)
}
