//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPriorityLifecycleDurableOutboxScopeAndDeadline(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	user := mustCreateUser(t, tx.Client(), &service.User{Email: fmt.Sprintf("lifecycle-%d@test.invalid", time.Now().UnixNano())})
	key := mustCreateApiKey(t, tx.Client(), &service.APIKey{UserID: user.ID, Key: fmt.Sprintf("synthetic-lifecycle-%d", time.Now().UnixNano()), Name: "synthetic"})
	account := mustCreateAccount(t, tx.Client(), &service.Account{Name: "lifecycle disabled remains visible", Platform: service.PlatformOpenAI})
	model := fmt.Sprintf("lifecycle-model-%d", time.Now().UnixNano())
	endpoint := "/v1/responses"
	cfg := service.DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	cfg.MinSamples = 1
	measurement, spec, _ := service.PriorityLifecycleVersions(cfg)
	scope := service.PriorityLifecycleScope{Model: model, Endpoint: endpoint, RequestType: service.RequestTypeStream, MeasurementSchema: measurement, StatisticsSpec: spec}
	writer := newUsageLogRepositoryWithSQL(tx.Client(), tx)
	row := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: model + "-empty", Model: model, RequestedModel: model, InboundEndpoint: &endpoint, InputTokens: 1, Stream: true, RequestType: service.RequestTypeStream, CreatedAt: time.Now().Add(-time.Minute)}
	inserted, err := writer.Create(ctx, row)
	require.NoError(t, err)
	require.True(t, inserted)
	var pending int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM priority_lifecycle_outbox WHERE usage_log_id=$1", row.ID).Scan(&pending))
	require.Zero(t, pending, "uncommitted usage must not publish an outbox event")
	require.NoError(t, tx.Commit())
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("DELETE FROM priority_lifecycle_statistics WHERE scope_key=$1", scope.Key())
		_, _ = integrationDB.Exec("DELETE FROM priority_lifecycle_policy WHERE scope_key=$1", scope.PolicyKey())
		_, _ = integrationDB.Exec("DELETE FROM priority_lifecycle_policy_events WHERE scope_key=$1", scope.PolicyKey())
		_, _ = integrationDB.Exec("DELETE FROM priority_lifecycle_outbox WHERE usage_log_id IN(SELECT id FROM usage_logs WHERE account_id=$1)", account.ID)
		_, _ = integrationDB.Exec("DELETE FROM usage_logs WHERE account_id=$1", account.ID)
		_, _ = integrationDB.Exec("DELETE FROM accounts WHERE id=$1", account.ID)
		_, _ = integrationDB.Exec("DELETE FROM api_keys WHERE id=$1", key.ID)
		_, _ = integrationDB.Exec("DELETE FROM users WHERE id=$1", user.ID)
	})
	repo := newUsageLogRepositoryWithSQL(integrationEntClient, integrationDB)
	require.NoError(t, repo.AdvancePriorityLifecycle(ctx, cfg))
	empty, policies, err := repo.ReadPriorityLifecycle(ctx, scope)
	require.NoError(t, err)
	require.NotNil(t, empty)
	require.Empty(t, policies)
	require.Zero(t, empty.Signals[account.ID].Samples)
	require.Equal(t, 1, empty.Signals[account.ID].LatencyUnknownSamples)
	slow := 20000
	row.ID = 0
	row.RequestID = model + "-slow"
	row.FirstTokenMs = &slow
	inserted, err = repo.Create(ctx, row)
	require.NoError(t, err)
	require.True(t, inserted)
	// A different ingress endpoint must not join this measured population.
	chat := "/v1/chat/completions"
	other := *row
	other.ID = 0
	other.RequestID = model + "-other-endpoint"
	other.InboundEndpoint = &chat
	fast := 100
	other.FirstTokenMs = &fast
	_, err = repo.Create(ctx, &other)
	require.NoError(t, err)
	require.NoError(t, repo.AdvancePriorityLifecycle(ctx, cfg))
	stats, policies, err := repo.ReadPriorityLifecycle(ctx, scope)
	require.NoError(t, err)
	require.NotNil(t, stats)
	require.Len(t, policies, 1)
	require.Equal(t, 1, stats.Signals[account.ID].Samples)
	require.InDelta(t, 20000, stats.Signals[account.ID].P90TTFTMs, .01)
	require.Greater(t, stats.Generation, empty.Generation)
	until, evidence := policies[0].ExpiresAt, policies[0].EvidenceID
	// Re-read, worker restart, and a policy-only configuration change cannot
	// manufacture new evidence or restart an absolute deadline.
	cfg.LatencyPenaltyMinutes = 30
	cfg.CostWeight = 35
	restart := newUsageLogRepositoryWithSQL(integrationEntClient, integrationDB)
	require.NoError(t, restart.AdvancePriorityLifecycle(ctx, cfg))
	next, policies, err := restart.ReadPriorityLifecycle(ctx, scope)
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Equal(t, until, policies[0].ExpiresAt)
	require.Equal(t, evidence, policies[0].EvidenceID)
	beforeGeneration := next.Generation
	_, _, err = restart.ReadPriorityLifecycle(ctx, scope)
	require.NoError(t, err)
	after, _, err := restart.ReadPriorityLifecycle(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, beforeGeneration, after.Generation, "GET/read must not publish statistics")
	events, err := repo.ReadPriorityLifecycleEvents(ctx, scope, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, evidence, events[0].Policy.EvidenceID)
}
