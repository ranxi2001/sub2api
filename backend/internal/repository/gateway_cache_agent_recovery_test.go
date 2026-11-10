package repository

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIssue715RedisOwnerSharedExpiryAndConflicts(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	other := &gatewayCache{rdb: cache.rdb}
	ctx := context.Background()
	key := strings.Repeat("a", 64)
	require.NoError(t, cache.PublishOpenAIAgentRecovery(ctx, key, "route-one", time.Minute))
	value, err := other.GetOpenAIAgentRecovery(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "route-one", value, "independent adapters must share application-restart ownership")
	var wg sync.WaitGroup
	for _, route := range []string{"route-one", "route-two"} {
		wg.Add(1)
		go func(route string) {
			defer wg.Done()
			require.NoError(t, other.PublishOpenAIAgentRecovery(ctx, key, route, 2*time.Minute))
		}(route)
	}
	wg.Wait()
	require.NoError(t, cache.PublishOpenAIAgentRecovery(ctx, key, "route-one", time.Second))
	value, err = other.GetOpenAIAgentRecovery(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "conflict", value, "a later success must not overwrite a conflicting owner")
	mr.FastForward(90 * time.Second)
	value, err = cache.GetOpenAIAgentRecovery(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "conflict", value, "refresh must not shorten conflict retention")
	mr.FastForward(time.Minute)
	value, err = other.GetOpenAIAgentRecovery(ctx, key)
	require.NoError(t, err)
	require.Empty(t, value, "expired evidence is not an ownership grant")
	require.NoError(t, cache.PublishOpenAIAgentRecovery(ctx, key, "route-one", time.Minute))
	mr.Close()
	_, err = cache.GetOpenAIAgentRecovery(ctx, key)
	require.Error(t, err, "Redis errors cannot be downgraded to cache misses")
}

func TestIssue715RedisDeliveryTokenCAS(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	other := &gatewayCache{rdb: cache.rdb}
	ctx := context.Background()
	key := strings.Repeat("c", 64)
	reserve := func(token string) error {
		return cache.ReserveOpenAIAgentRecovery(ctx, key, "route-one", token, time.Minute)
	}
	finish := func(token string, commit bool) error {
		return other.FinishOpenAIAgentRecovery(ctx, key, "route-one", token, commit, time.Minute)
	}
	value := func() string { raw, err := cache.GetOpenAIAgentRecovery(ctx, key); require.NoError(t, err); return raw }
	require.NoError(t, reserve("pending:first"))
	require.Equal(t, "pending:first", value())
	require.Error(t, finish("pending:foreign", false), "foreign abort cannot revoke producer")
	require.Equal(t, "pending:first", value())
	require.NoError(t, finish("pending:first", true))
	require.Equal(t, "route-one", value())
	require.NoError(t, reserve("pending:second"))
	require.NoError(t, finish("pending:second", false))
	require.Equal(t, "route-one", value(), "failed later producer must preserve an existing successful grant")
	mr.Del(openAIAgentRecoveryPrefix + key)
	require.NoError(t, reserve("pending:first"))
	require.NoError(t, finish("pending:first", false))
	require.Equal(t, "failed", value())
	require.Error(t, finish("pending:first", true), "late commit cannot resurrect aborted output")
	require.Equal(t, "failed", value())
	mr.Del(openAIAgentRecoveryPrefix + key)
	require.NoError(t, reserve("pending:first"))
	require.Error(t, reserve("pending:second"))
	require.Equal(t, "conflict", value(), "concurrent producers are never last-writer-wins")
	require.Error(t, finish("pending:first", true))
	require.Equal(t, "conflict", value())
}

func TestIssue758RedisOwnerRetentionSurvivesQuotaReset(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	other := &gatewayCache{rdb: cache.rdb}
	ctx := context.Background()
	key := strings.Repeat("d", 64)
	redisKey := openAIAgentRecoveryPrefix + key
	require.NoError(t, cache.PublishOpenAIAgentRecovery(ctx, key, "route-one", time.Hour))
	mr.FastForward(30 * time.Minute)

	// The owner's remaining five-hour quota window outlives the original
	// affinity TTL. Keep one more hour after reset for the next continuation.
	refreshed, err := other.RefreshOpenAIAgentRecovery(ctx, key, "route-one", 6*time.Hour)
	require.NoError(t, err)
	require.True(t, refreshed)
	mr.FastForward(5 * time.Hour)
	value, err := cache.GetOpenAIAgentRecovery(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "route-one", value, "ownership must survive until quota recovery across adapters")

	remaining := mr.TTL(redisKey)
	refreshed, err = cache.RefreshOpenAIAgentRecovery(ctx, key, "route-one", time.Minute)
	require.NoError(t, err)
	require.True(t, refreshed)
	require.Equal(t, remaining, mr.TTL(redisKey), "a shorter refresh must not shorten the recovery grace period")
	mr.FastForward(59 * time.Minute)
	require.True(t, mr.Exists(redisKey))
	mr.FastForward(2 * time.Minute)
	refreshed, err = other.RefreshOpenAIAgentRecovery(ctx, key, "route-one", 6*time.Hour)
	require.NoError(t, err)
	require.False(t, refreshed)
	require.False(t, mr.Exists(redisKey), "expired evidence must never be recreated by refresh")
}

func TestIssue758RedisOwnerRetentionPreservesOtherStates(t *testing.T) {
	for _, old := range []string{"", "route-two", "failed", "conflict", "pending:fixture:route-one"} {
		t.Run(old, func(t *testing.T) {
			cache, mr := newHTTPResponseOwnerTestCache(t)
			ctx := context.Background()
			key := strings.Repeat("e", 64)
			redisKey := openAIAgentRecoveryPrefix + key
			if old != "" {
				require.NoError(t, cache.rdb.Set(ctx, redisKey, old, time.Hour).Err())
			}
			remaining := mr.TTL(redisKey)
			refreshed, err := cache.RefreshOpenAIAgentRecovery(ctx, key, "route-one", 6*time.Hour)
			require.NoError(t, err)
			require.False(t, refreshed)
			value, err := cache.GetOpenAIAgentRecovery(ctx, key)
			require.NoError(t, err)
			require.Equal(t, old, value)
			require.Equal(t, remaining, mr.TTL(redisKey), "a stale waiter cannot retain a different delivery state")

			if old == "failed" || old == "conflict" || strings.HasPrefix(old, "pending:") {
				refreshed, err = cache.RefreshOpenAIAgentRecovery(ctx, key, old, 6*time.Hour)
				require.NoError(t, err)
				require.False(t, refreshed, "non-proven states cannot be retained as ownership evidence")
				require.Equal(t, remaining, mr.TTL(redisKey))
			}
		})
	}
}

func TestIssue758RedisOwnerRetentionConcurrentConflict(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	ctx := context.Background()
	key := strings.Repeat("f", 64)
	require.NoError(t, cache.PublishOpenAIAgentRecovery(ctx, key, "route-one", time.Hour))
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, err := cache.RefreshOpenAIAgentRecovery(ctx, key, "route-one", 6*time.Hour)
		errs <- err
	}()
	go func() {
		<-start
		errs <- cache.PublishOpenAIAgentRecovery(ctx, key, "route-two", time.Hour)
	}()
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	value, err := cache.GetOpenAIAgentRecovery(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "conflict", value, "refresh cannot overwrite a concurrent conflicting observation")
	remaining := mr.TTL(openAIAgentRecoveryPrefix + key)
	refreshed, err := cache.RefreshOpenAIAgentRecovery(ctx, key, "route-one", 6*time.Hour)
	require.NoError(t, err)
	require.False(t, refreshed)
	require.Equal(t, remaining, mr.TTL(openAIAgentRecoveryPrefix+key))
}
