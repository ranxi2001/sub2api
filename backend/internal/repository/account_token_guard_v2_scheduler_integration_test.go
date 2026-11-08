//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newGuardSchedulerAccount(t *testing.T, nextProbe time.Time, enabled bool) int64 {
	t.Helper()
	account := mustCreateAccount(t, testEntClient(t), &service.Account{
		Name: "credential-guard-scheduler", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "synthetic"},
	})
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, account.ID)
		require.NoError(t, err)
	})
	guard := NewAccountTokenGuardV2Repository(integrationDB)
	require.NoError(t, guard.UpsertAccount(context.Background(), account.ID, enabled, true))
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE account_token_guard_v2_accounts SET next_probe_at=$2 WHERE account_id=$1`, account.ID, nextProbe)
	require.NoError(t, err)
	return account.ID
}

func guardSchedulerDatabaseNow(t *testing.T) time.Time {
	t.Helper()
	var now time.Time
	require.NoError(t, integrationDB.QueryRow(`SELECT clock_timestamp()`).Scan(&now))
	return now
}

func TestAccountTokenGuardV2ClaimDueHonorsCycleCutoffAndDatabaseTime(t *testing.T) {
	ctx := context.Background()
	now := guardSchedulerDatabaseNow(t)
	cutoff := now.Add(-time.Minute)
	oldID := newGuardSchedulerAccount(t, cutoff.Add(-time.Second), true)
	newID := newGuardSchedulerAccount(t, cutoff.Add(30*time.Second), true)
	futureID := newGuardSchedulerAccount(t, now.Add(2*time.Hour), true)
	newGuardSchedulerAccount(t, cutoff.Add(-time.Second), false)
	guard := NewAccountTokenGuardV2Repository(integrationDB)

	claimed, err := guard.ClaimDue(ctx, "cutoff-old", time.Minute, 10, cutoff)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "accounts becoming due after this cycle started wait for the next cycle")
	require.Equal(t, oldID, claimed[0].AccountID)
	require.NoError(t, guard.ReleaseProbe(ctx, oldID, "cutoff-old"))

	// Even a future caller cutoff cannot turn the repository into a claimant
	// for accounts that have not reached their actual scheduled time.
	claimed, err = guard.ClaimDue(ctx, "cutoff-future", time.Minute, 10, now.Add(3*time.Hour))
	require.NoError(t, err)
	var ids []int64
	for _, record := range claimed {
		ids = append(ids, record.AccountID)
	}
	require.ElementsMatch(t, []int64{oldID, newID}, ids)
	record, err := guard.GetAccount(ctx, futureID)
	require.NoError(t, err)
	require.Empty(t, record.LeaseOwner)
}

func TestAccountTokenGuardV2ExpiredClaimCannotCompleteOrReleaseReplacement(t *testing.T) {
	ctx := context.Background()
	now := guardSchedulerDatabaseNow(t)
	id := newGuardSchedulerAccount(t, now.Add(-time.Minute), true)
	guard := NewAccountTokenGuardV2Repository(integrationDB)
	_, err := integrationDB.ExecContext(ctx, `UPDATE account_token_guard_v2_accounts
		SET probe_state='auth', probe_detail='credential was rejected', fail_streak=7,
			last_probe_at=$2, blocked_reason='automatic re-login cooling down'
		WHERE account_id=$1`, id, now.Add(-2*time.Minute))
	require.NoError(t, err)
	before, err := guard.GetAccount(ctx, id)
	require.NoError(t, err)
	oldClaim, err := guard.ClaimAccount(ctx, id, "claim-old", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, oldClaim)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_token_guard_v2_accounts
		SET lease_until=NOW()-INTERVAL '1 second' WHERE account_id=$1`, id)
	require.NoError(t, err)
	completion := service.AccountTokenGuardV2ProbeCompletion{
		ProbeState: service.AccountTokenGuardV2ProbeOK, LastProbeAt: now, NextProbeAt: now.Add(time.Hour),
	}
	require.ErrorIs(t, guard.CompleteProbe(ctx, id, "claim-old", completion), sql.ErrNoRows,
		"a stale result must be rejected even before a replacement is claimed")

	newClaim, err := guard.ClaimAccount(ctx, id, "claim-new", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, newClaim)
	require.ErrorIs(t, guard.CompleteProbe(ctx, id, "claim-old", completion), sql.ErrNoRows)
	require.NoError(t, guard.ReleaseProbe(ctx, id, "claim-old"), "a late release is harmless and idempotent")
	stored, err := guard.GetAccount(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "claim-new", stored.LeaseOwner)
	require.Equal(t, newClaim.LeaseUntil, stored.LeaseUntil)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, guard.ReleaseProbe(canceled, id, "claim-new"), context.Canceled)
	require.NoError(t, guard.ReleaseProbe(context.WithoutCancel(canceled), id, "claim-new"),
		"cleanup must remain possible after the cycle is canceled")
	require.NoError(t, guard.ReleaseProbe(ctx, id, "claim-new"), "repeated cleanup is harmless")
	stored, err = guard.GetAccount(ctx, id)
	require.NoError(t, err)
	require.Empty(t, stored.LeaseOwner)
	require.Nil(t, stored.LeaseUntil)
	require.Equal(t, before.ProbeState, stored.ProbeState)
	require.Equal(t, before.ProbeDetail, stored.ProbeDetail)
	require.Equal(t, before.FailStreak, stored.FailStreak)
	require.Equal(t, before.LastProbeAt, stored.LastProbeAt)
	require.Equal(t, before.NextProbeAt, stored.NextProbeAt)
	require.Equal(t, before.BlockedReason, stored.BlockedReason)
	claimed, err := guard.ClaimDue(ctx, "claim-retry", time.Minute, 1, now)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "release does not postpone the unfinished inspection")
	require.Equal(t, id, claimed[0].AccountID)
}

func TestAccountTokenGuardV2ConcurrentClaimsInspectAccountOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cutoff := guardSchedulerDatabaseNow(t).Add(-time.Hour)
	id := newGuardSchedulerAccount(t, cutoff.Add(-time.Second), true)
	guard := NewAccountTokenGuardV2Repository(integrationDB)
	type claimResult struct {
		owner   string
		records []service.AccountTokenGuardV2Record
		err     error
	}
	results := make(chan claimResult, 8)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			owner := fmt.Sprintf("claim-racer-%d", i)
			records, err := guard.ClaimDue(ctx, owner, time.Minute, 1, cutoff)
			results <- claimResult{owner: owner, records: records, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	count := 0
	for result := range results {
		require.NoError(t, result.err)
		for _, record := range result.records {
			count++
			require.Equal(t, id, record.AccountID)
			require.Equal(t, result.owner, record.LeaseOwner)
		}
	}
	require.Equal(t, 1, count, "concurrent workers must never inspect the same live lease")
}

func TestAccountTokenGuardV2ClaimDueCanDrainThirtyAccounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cutoff := guardSchedulerDatabaseNow(t).Add(-time.Hour)
	want := make(map[int64]bool)
	for range 30 {
		want[newGuardSchedulerAccount(t, cutoff.Add(-time.Second), true)] = true
	}
	guard := NewAccountTokenGuardV2Repository(integrationDB)
	seen := make(map[int64]bool)
	for i := 0; i < 31; i++ {
		owner := fmt.Sprintf("claim-drain-%d", i)
		claimed, err := guard.ClaimDue(ctx, owner, time.Minute, 1, cutoff)
		require.NoError(t, err)
		if len(claimed) == 0 {
			break
		}
		require.Len(t, claimed, 1)
		record := claimed[0]
		require.True(t, want[record.AccountID])
		require.False(t, seen[record.AccountID], "one cycle must not reclaim an account it completed")
		require.Equal(t, owner, record.LeaseOwner)
		seen[record.AccountID] = true
		now := time.Now()
		require.NoError(t, guard.CompleteProbe(ctx, record.AccountID, owner, service.AccountTokenGuardV2ProbeCompletion{
			ProbeState: service.AccountTokenGuardV2ProbeOK, LastProbeAt: now, NextProbeAt: now.Add(time.Minute),
		}))
	}
	require.Len(t, seen, 30, "a cycle can continue beyond the old ten-account batch")
}
