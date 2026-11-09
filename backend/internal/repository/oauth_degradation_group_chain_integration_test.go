//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuthDegradationGroupOutboxSchedulerChain(t *testing.T) {
	ctx := t.Context()
	a, closed := degradationChainFixture(t, 20, false)
	kept := mustCreateGroup(t, integrationEntClient, &service.Group{Name: fmt.Sprintf("chain-kept-%d", a.ID), Platform: service.PlatformOpenAI, Status: service.StatusActive})
	survivor := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "group-chain-survivor", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 5, Credentials: map[string]any{"api_key": "disposable-fixture"}})
	t.Cleanup(func() {
		for _, query := range []string{"DELETE FROM scheduler_outbox_retries WHERE account_id=$1", "DELETE FROM scheduler_outbox WHERE account_id=$1", "DELETE FROM accounts WHERE id=$1"} {
			_, err := integrationDB.ExecContext(context.Background(), query, survivor.ID)
			require.NoError(t, err)
		}
		require.NoError(t, integrationEntClient.Group.DeleteOneID(kept.ID).Exec(context.Background()))
	})
	rdb, setWrites, observed := degradationChainRedis(t)
	cache := NewSchedulerCache(rdb)
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, cache)
	require.NoError(t, repo.BindGroups(ctx, a.ID, []int64{closed.ID, kept.ID}))
	require.NoError(t, repo.SetGroupAllowedModels(ctx, a.ID, map[int64][]string{kept.ID: {"gpt-6.1-terra"}}))
	require.NoError(t, repo.BindGroups(ctx, survivor.ID, []int64{closed.ID}))
	var raw string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", service.SettingKeyOAuthAutoConfig).Scan(&raw))
	var policy service.OAuthAutoConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &policy))
	policy.DegradationAction.LimitConcurrency, policy.DegradationAction.LimitModels = degradationActionFlag(false), degradationActionFlag(false)
	policy.DegradationAction.LimitGroups, policy.DegradationAction.GroupIDs = true, []int64{closed.ID}
	encoded, err := json.Marshal(policy)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, string(encoded))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE account_id=$1 OR account_id=$2", a.ID, survivor.ID)
	require.NoError(t, err)
	outbox := NewSchedulerOutboxRepository(integrationDB)
	initial, err := outbox.MaxID(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.SetOutboxWatermark(ctx, initial))
	groups := NewGroupRepository(integrationEntClient, integrationDB)
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.DbFallbackEnabled = true
	cfg.Gateway.Scheduling.OutboxPollIntervalSeconds = 1
	warm := service.NewSchedulerSnapshotService(cache, outbox, repo, groups, cfg)
	for _, group := range []*service.Group{closed, kept} {
		accounts, _, err := warm.ListSchedulableAccounts(ctx, &group.ID, service.PlatformOpenAI, true)
		require.NoError(t, err)
		want := 1
		if group.ID == closed.ID {
			want = 2
		}
		require.Len(t, accounts, want)
	}
	before := degradationGateway(t, repo, warm, rdb)
	for _, group := range []*service.Group{closed, kept} {
		selection, _, err := before.SelectAccountWithScheduler(ctx, &group.ID, "", "", "gpt-6.1-terra", map[int64]struct{}{survivor.ID: {}}, service.OpenAIUpstreamTransportHTTPSSE, false)
		require.NoError(t, err)
		require.True(t, selection.Acquired)
		require.Equal(t, a.ID, selection.Account.ID)
		selection.ReleaseFunc()
	}
	setWrites(false)
	ops := service.NewAccountOpsService(NewSettingRepository(integrationEntClient), NewAccountOpsRepository(integrationDB), nil)
	ops.SetNotificationDependencies(repo, nil, false, "UTC")
	ops.Start()
	defer ops.Stop()
	require.Eventually(t, func() bool {
		var applied, bound bool
		err := integrationDB.QueryRowContext(ctx, "SELECT extra ? 'oauth_degradation_action',EXISTS(SELECT 1 FROM account_groups WHERE account_id=$1 AND group_id=$2) FROM accounts WHERE id=$1", a.ID, closed.ID).Scan(&applied, &bound)
		return err == nil && applied && !bound
	}, 8*time.Second, 20*time.Millisecond)
	ops.Stop()
	require.Positive(t, observed.denied.Load(), "real Redis must reject post-commit publication")
	var groupEvent, maxEvent int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM scheduler_outbox WHERE account_id=$1 AND event_type='account_groups_changed' AND payload->'group_ids' @> jsonb_build_array($2::bigint)", a.ID, closed.ID).Scan(&groupEvent))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT MAX(id) FROM scheduler_outbox WHERE account_id=$1", a.ID).Scan(&maxEvent))
	var eventCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type IN ('account_changed','account_groups_changed')", a.ID).Scan(&eventCount))
	require.Equal(t, 2, eventCount)
	old, hit, err := cache.GetSnapshot(ctx, service.SchedulerBucket{GroupID: closed.ID, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeForced})
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, old, 2, "failed publication leaves the old closed-group candidate as evidence")
	startSnapshots := func() (*service.SchedulerSnapshotService, func()) {
		gate := &degradationStartupGate{SchedulerCache: NewSchedulerCache(rdb), entered: make(chan struct{}), release: make(chan struct{})}
		s := service.NewSchedulerSnapshotService(gate, outbox, repo, groups, cfg)
		var once sync.Once
		release := func() { once.Do(func() { close(gate.release) }) }
		t.Cleanup(func() { release(); s.Stop() })
		s.Start()
		select {
		case <-gate.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("startup rebuild did not enter the gate")
		}
		return s, release
	}
	snapshots, release := startSnapshots()
	require.Eventually(t, func() bool {
		var n int
		return integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox_retries WHERE event_id=$1 AND lease_until IS NULL", groupEvent).Scan(&n) == nil && n == 1
	}, 12*time.Second, 20*time.Millisecond, "removed-group event must persist a real PostgreSQL retry")
	release()
	snapshots.Stop()
	repo = newAccountRepositoryWithSQL(integrationEntClient, integrationDB, NewSchedulerCache(rdb))
	outbox = NewSchedulerOutboxRepository(integrationDB)
	snapshots, release = startSnapshots()
	setWrites(true)
	require.Eventually(t, func() bool {
		var n int
		err := integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox_retries WHERE account_id=$1", a.ID).Scan(&n)
		watermark, cacheErr := cache.GetOutboxWatermark(ctx)
		return err == nil && cacheErr == nil && n == 0 && watermark >= maxEvent
	}, 20*time.Second, 20*time.Millisecond)
	reader := service.NewSchedulerSnapshotService(NewSchedulerCache(rdb), outbox, repo, groups, &config.Config{})
	closedAccounts, _, err := reader.ListSchedulableAccounts(ctx, &closed.ID, service.PlatformOpenAI, true)
	require.NoError(t, err, "fresh reader has database fallback disabled")
	require.Len(t, closedAccounts, 1)
	require.Equal(t, survivor.ID, closedAccounts[0].ID)
	keptAccounts, _, err := reader.ListSchedulableAccounts(ctx, &kept.ID, service.PlatformOpenAI, true)
	require.NoError(t, err)
	require.Len(t, keptAccounts, 1)
	require.Equal(t, a.ID, keptAccounts[0].ID)
	require.Equal(t, 20, keptAccounts[0].Concurrency)
	require.Equal(t, []string{"gpt-6.1-terra"}, keptAccounts[0].GroupAllowedModels(kept.ID))
	fresh := degradationGateway(t, repo, reader, rdb)
	for _, pair := range [][2]int64{{closed.ID, survivor.ID}, {kept.ID, a.ID}} {
		selection, _, err := fresh.SelectAccountWithScheduler(ctx, &pair[0], "", "", "gpt-6.1-terra", nil, service.OpenAIUpstreamTransportHTTPSSE, false)
		require.NoError(t, err)
		require.True(t, selection.Acquired)
		require.Equal(t, pair[1], selection.Account.ID)
		selection.ReleaseFunc()
	}
	selection, _, err := fresh.SelectAccountWithScheduler(ctx, &kept.ID, "", "", "gpt-6-luna", nil, service.OpenAIUpstreamTransportHTTPSSE, false)
	require.ErrorIs(t, err, service.ErrNoAvailableAccounts)
	require.Nil(t, selection, "retained group's model restriction still applies")
	require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
	history, _ := degradationRows(t, a.ID)
	require.Equal(t, 1, history)
	t.Logf("group outbox=%d recovered after service restart with startup gated; closed bucket selects survivor=%d, kept selects account=%d concurrency=20", groupEvent, survivor.ID, a.ID)
	release()
	snapshots.Stop()
}
