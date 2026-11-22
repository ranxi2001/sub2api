//go:build integration

package repository

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func stageBillingBatchFixture(t *testing.T, repo *usageBillingRepository, cmd service.UsageBillingCommand, usage service.UsageLog, n int) []service.UsageBillingEventKey {
	t.Helper()
	keys := make([]service.UsageBillingEventKey, n)
	for i := range keys {
		cmd.RequestID = uuid.NewString()
		cmd.RequestFingerprint = ""
		cmd.BalanceCost, cmd.APIKeyQuotaCost = 0.1, 0.1
		quote := 0.1
		cmd.QuotedActualCost = &quote
		usage.RequestID, usage.ActualCost = cmd.RequestID, 0.1
		require.NoError(t, repo.StageUsageBilling(context.Background(), &cmd, &usage))
		keys[i] = service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID}
	}
	return keys
}

func TestStagedBillingBatchAggregatesWalletAndKeyAndReplay(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	keys := stageBillingBatchFixture(t, repo, cmd, usage, 8)
	name := "batch_writes_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := repo.db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE %s(kind text);
	 CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 IF (TG_TABLE_NAME='users' AND NEW.id=%d) OR (TG_TABLE_NAME='api_keys' AND NEW.id=%d) THEN INSERT INTO %s VALUES(TG_TABLE_NAME); END IF; RETURN NEW; END $$;
	 CREATE TRIGGER %s AFTER UPDATE ON users FOR EACH ROW EXECUTE FUNCTION %s();
	 CREATE TRIGGER %s AFTER UPDATE ON api_keys FOR EACH ROW EXECUTE FUNCTION %s();`, name, name, cmd.UserID, cmd.APIKeyID, name, name, name, name, name))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = repo.db.ExecContext(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON users; DROP TRIGGER IF EXISTS %s ON api_keys; DROP FUNCTION IF EXISTS %s(); DROP TABLE IF EXISTS %s`, name, name, name, name))
	})
	out, err := repo.applyStagedUsageBillingBatch(ctx, strconv.FormatInt(cmd.UserID, 10), keys)
	require.NoError(t, err)
	require.Len(t, out, 8)
	for i, r := range out {
		require.True(t, r.Settled)
		require.True(t, r.Result.Applied)
		require.InDelta(t, 100-float64(i+1)/10, *r.Result.NewBalance, 1e-8)
		require.NoError(t, repo.AcknowledgeUsageBillingCache(ctx, keys[i]))
	}
	var balance, quota string
	var writes, logs, quotes int
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT balance::text FROM users WHERE id=$1`, cmd.UserID).Scan(&balance))
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT quota_used::text FROM api_keys WHERE id=$1`, cmd.APIKeyID).Scan(&quota))
	require.Equal(t, "99.20000000", balance)
	require.Equal(t, "0.80000000", quota)
	for _, kind := range []string{"users", "api_keys"} {
		require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM `+name+` WHERE kind=$1`, kind).Scan(&writes))
		require.Equal(t, 1, writes, "eight commands must update each hot row once")
	}
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM usage_logs WHERE user_id=$1`, cmd.UserID).Scan(&logs))
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM usage_billing_quotes WHERE api_key_id=$1`, cmd.APIKeyID).Scan(&quotes))
	require.Equal(t, 8, logs)
	require.Equal(t, 8, quotes)
	out, err = repo.applyStagedUsageBillingBatch(ctx, strconv.FormatInt(cmd.UserID, 10), keys)
	require.NoError(t, err)
	for _, r := range out {
		require.True(t, r.Settled)
		require.False(t, r.Result.Applied)
		require.False(t, r.NeedsCache)
	}
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM `+name).Scan(&writes))
	require.Equal(t, 2, writes, "replay cannot debit either hot row")
}

func TestStagedBillingBatchFailureRollsBackAllEffects(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	keys := stageBillingBatchFixture(t, repo, cmd, usage, 3)
	name := "batch_failure_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := repo.db.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.request_id='%s' THEN RAISE EXCEPTION 'injected batch failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON usage_logs FOR EACH ROW EXECUTE FUNCTION %s()`, name, keys[1].RequestID, name, name))
	require.NoError(t, err)
	cleanup := func() {
		_, e := repo.db.ExecContext(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON usage_logs; DROP FUNCTION IF EXISTS %s()`, name, name))
		require.NoError(t, e)
	}
	t.Cleanup(cleanup)
	_, err = repo.applyStagedUsageBillingBatch(ctx, strconv.FormatInt(cmd.UserID, 10), keys)
	require.ErrorContains(t, err, "injected batch failure")
	var balance string
	var logs, settled, claims, attempts int
	var retry time.Time
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT balance::text FROM users WHERE id=$1`, cmd.UserID).Scan(&balance))
	require.Equal(t, "100.00000000", balance)
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM usage_logs WHERE user_id=$1`, cmd.UserID).Scan(&logs))
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM usage_billing_events WHERE api_key_id=$1 AND state<>'pending'`, cmd.APIKeyID).Scan(&settled))
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT min(next_attempt_at),max(attempts) FROM usage_billing_events WHERE api_key_id=$1`, cmd.APIKeyID).Scan(&retry, &attempts))
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=$1`, cmd.APIKeyID).Scan(&claims))
	require.Zero(t, logs)
	require.Zero(t, settled)
	require.Zero(t, claims)
	require.Equal(t, 1, attempts)
	require.True(t, retry.After(time.Now().Add(29*time.Second)))
	cleanup()
	// Repeated keys inside a batch are only settled and notified once.
	keys = append(keys, keys[0])
	out, err := repo.applyStagedUsageBillingBatch(ctx, strconv.FormatInt(cmd.UserID, 10), keys)
	require.NoError(t, err)
	require.False(t, out[3].Result.Applied)
	require.True(t, out[3].Settled)
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT balance::text FROM users WHERE id=$1`, cmd.UserID).Scan(&balance))
	require.Equal(t, "99.70000000", balance)
}

func TestStagedBillingBatchAutoRechargeUsesClosingBalanceOnce(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	_, err := repo.db.ExecContext(ctx, `UPDATE users SET balance=0.25 WHERE id=$1;
        `, cmd.UserID)
	require.NoError(t, err)
	_, err = repo.db.ExecContext(ctx, `INSERT INTO auto_credit_config(user_id,enabled,threshold,amount,cap) VALUES($1,true,1,2,10)`, cmd.UserID)
	require.NoError(t, err)
	keys := stageBillingBatchFixture(t, repo, cmd, usage, 4)
	out, err := repo.applyStagedUsageBillingBatch(ctx, strconv.FormatInt(cmd.UserID, 10), keys)
	require.NoError(t, err)
	for _, r := range out[:3] {
		require.False(t, r.Result.AutoCreditIssued)
	}
	require.True(t, out[3].Result.AutoCreditIssued)
	require.InDelta(t, 1.85, *out[3].Result.NewBalance, 1e-8)
	var count, epoch int
	var balance, before, identity string
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT balance::text FROM users WHERE id=$1`, cmd.UserID).Scan(&balance))
	require.Equal(t, "1.85000000", balance)
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM auto_credit_ledger WHERE user_id=$1`, cmd.UserID).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT debit_epoch FROM auto_credit_config WHERE user_id=$1`, cmd.UserID).Scan(&epoch))
	require.Equal(t, 1, epoch, "a recharge is evaluated once per settlement batch")
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT balance_before::text,trigger_identity FROM auto_credit_ledger WHERE user_id=$1`, cmd.UserID).Scan(&before, &identity))
	require.Equal(t, "-0.15000000", before)
	require.Equal(t, fmt.Sprintf("usage:%d:%s", cmd.APIKeyID, keys[3].RequestID), identity)
	_, err = repo.applyStagedUsageBillingBatch(ctx, strconv.FormatInt(cmd.UserID, 10), keys)
	require.NoError(t, err)
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM auto_credit_ledger WHERE user_id=$1`, cmd.UserID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestStagedBillingBatchTwoProcessesSettleOnce(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	keys := stageBillingBatchFixture(t, repo, cmd, usage, 12)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other := &usageBillingRepository{db: repo.db}
			_, err := other.applyStagedUsageBillingBatch(ctx, strconv.FormatInt(cmd.UserID, 10), keys)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var balance string
	var logs int
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT balance::text FROM users WHERE id=$1`, cmd.UserID).Scan(&balance))
	require.Equal(t, "98.80000000", balance)
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM usage_logs WHERE user_id=$1`, cmd.UserID).Scan(&logs))
	require.Equal(t, 12, logs)
}

func TestStagedBillingPublicPathBatchesArrivalsBehindOwner(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	keys := stageBillingBatchFixture(t, repo, cmd, usage, 9)
	owner := strconv.FormatInt(cmd.UserID, 10)
	hold, err := repo.admission.acquire(ctx, owner, repo.db.Stats().MaxOpenConnections)
	require.NoError(t, err)
	released := false
	t.Cleanup(func() {
		if !released {
			hold()
		}
	})
	results := make(chan error, len(keys))
	go func() { _, err := repo.ApplyStagedUsageBilling(ctx, keys[0]); results <- err }()
	requireBillingOwnerRefs(t, repo, cmd.UserID, 2)
	for _, key := range keys[1:] {
		go func() { _, err := repo.ApplyStagedUsageBilling(ctx, key); results <- err }()
	}
	require.Eventually(t, func() bool {
		repo.stagedLanes.mu.Lock()
		defer repo.stagedLanes.mu.Unlock()
		return len(repo.stagedLanes.owners[owner]) == 8
	}, 5*time.Second, time.Millisecond)
	hold()
	released = true
	for range keys {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("staged lane did not finish")
		}
	}
	// A transaction timestamp is stored in applied_at: first immediate command
	// and the eight queued commands must produce exactly two commit groups.
	var commits, events int
	var balance string
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(DISTINCT applied_at),count(*) FROM usage_billing_events WHERE api_key_id=$1 AND state='applied'`, cmd.APIKeyID).Scan(&commits, &events))
	require.Equal(t, 2, commits)
	require.Equal(t, 9, events)
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT balance::text FROM users WHERE id=$1`, cmd.UserID).Scan(&balance))
	require.Equal(t, "99.10000000", balance)
}

func TestQuotaSaveUsesBillingLockOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo, cmd, _, client := durableBillingFixture(t)
	quota := NewUserPlatformQuotaRepository(client)
	limit := 10.0
	a := UserPlatformQuotaRecord{UserID: cmd.UserID, Platform: "openai", DailyLimitUSD: &limit}
	b := UserPlatformQuotaRecord{UserID: cmd.UserID, Platform: "anthropic", DailyLimitUSD: &limit}
	require.NoError(t, quota.UpsertForUser(ctx, cmd.UserID, []UserPlatformQuotaRecord{a, b}))
	tx, err := repo.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// Hold the first quota lock as a settler. An administrator retaining A and
	// deleting B must wait here before taking B, regardless of statement order.
	var pid int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid() FROM user_platform_quotas WHERE user_id=$1 ORDER BY id LIMIT 1 FOR UPDATE`, cmd.UserID).Scan(&pid))
	done := make(chan error, 1)
	go func() { done <- quota.UpsertForUser(ctx, cmd.UserID, []UserPlatformQuotaRecord{a}) }()
	require.Eventually(t, func() bool {
		var waiting bool
		err := repo.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting)
		return err == nil && waiting
	}, 3*time.Second, 10*time.Millisecond)
	_, err = tx.ExecContext(ctx, lockActivePlatformQuotasSQL, cmd.UserID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-done)
}

func TestRecoverySkipsLockedBusyWalletReceipt(t *testing.T) {
	repo, cmd, usage, _ := durableBillingFixture(t)
	ctx := context.Background()
	require.NoError(t, repo.StageUsageBilling(ctx, &cmd, &usage))
	tx, err := repo.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var state string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT state FROM usage_billing_events WHERE request_id=$1 AND api_key_id=$2 FOR UPDATE`, cmd.RequestID, cmd.APIKeyID).Scan(&state))
	// A live lane owns this wallet; the event lock above models its in-flight
	// transaction. Recovery must skip the lock, not consume its cycle waiting.
	repo.stagedLanes.owners = map[string][]*stagedBillingRequest{strconv.FormatInt(cmd.UserID, 10): nil}
	cycle, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	out, err := repo.RecoverStagedUsageBilling(cycle, service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID})
	require.NoError(t, err)
	require.Nil(t, out)
	require.NoError(t, cycle.Err())
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT state FROM usage_billing_events WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, cmd.APIKeyID).Scan(&state))
	require.Equal(t, "pending", state)
}
