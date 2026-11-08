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
