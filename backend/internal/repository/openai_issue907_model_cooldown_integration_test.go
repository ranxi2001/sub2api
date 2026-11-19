//go:build integration

package repository

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// The WS adapter tests cover event dispatch. This verifies its shared durable
// model policy with real PostgreSQL, outbox and independent Redis readers.
func TestIssue907ModelCooldownPersistsAcrossSchedulerReaders(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	account := mustCreateAccount(t, testEntClient(t), &service.Account{
		Name: "fixture-issue907", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"access_token": "fixture-token", "model_mapping": map[string]any{"fixture-alias": "gpt-5.4"}},
	})
	options := *integrationRedis.Options()
	peer := redis.NewClient(&options)
	t.Cleanup(func() { _ = peer.Close() })
	cache := NewSchedulerCache(integrationRedis)
	remoteCache := NewSchedulerCache(peer)
	repo := NewAccountRepository(integrationEntClient, integrationDB, cache)
	t.Cleanup(func() {
		_ = cache.DeleteAccount(context.Background(), account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id=$1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id=$1", account.ID)
	})
	require.NoError(t, cache.SetAccount(ctx, account))
	rates := service.NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	failure := []byte(`{"type":"error","status":400,"error":{"code":"model_not_found","message":"The 'gpt-5.4' model is not supported when using Codex with a ChatGPT account."}}`)
	require.True(t, rates.HandleUpstreamModelNotFound(ctx, account, "fixture-alias", http.StatusBadRequest, failure))
	persisted, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	cached, err := remoteCache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	for _, snapshot := range []*service.Account{persisted, cached} {
		require.True(t, snapshot.IsSchedulable())
		require.False(t, snapshot.IsSchedulableForModelWithContext(ctx, "fixture-alias"))
		require.False(t, snapshot.IsSchedulableForModelWithContext(ctx, "gpt-5.4"))
		require.True(t, snapshot.IsSchedulableForModelWithContext(ctx, "gpt-5.6"))
	}
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1", account.ID).Scan(&count))
	require.Positive(t, count, "other replicas must receive an account-changed event")
}
