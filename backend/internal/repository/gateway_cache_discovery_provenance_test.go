//go:build unit && lossyprotocol

package repository

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIDiscoveryProofCAS(t *testing.T) {
	cache, redis := newHTTPResponseOwnerTestCache(t)
	ctx := context.Background()
	scope := strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64)
	key, _, err := openAIToolProvenanceKeys(scope)
	require.NoError(t, err)
	old := `{"version":1,"call":"delivered-call-digest"}`
	require.NoError(t, redis.Set(key, old))
	redis.SetTTL(key, time.Minute)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for _, catalog := range []string{"catalog-one", "catalog-two"} {
		wg.Add(1)
		go func(value string) {
			defer wg.Done()
			updated, err := cache.CompareAndSwapOpenAIToolProvenance(ctx, scope, old, value, time.Hour)
			mu.Lock()
			defer mu.Unlock()
			require.NoError(t, err)
			if updated {
				wins++
			}
		}(catalog)
	}
	wg.Wait()
	require.Equal(t, 1, wins, "two simultaneous catalogs cannot both bind one delivered call")
	require.LessOrEqual(t, redis.TTL(key), time.Minute, "receipt must not extend call lifetime")
	redis.FastForward(2 * time.Minute)
	updated, err := cache.CompareAndSwapOpenAIToolProvenance(ctx, scope, old, "replacement", time.Hour)
	require.NoError(t, err)
	require.False(t, updated, "expired proof must not become a fresh first-result grant")
	require.False(t, redis.Exists(key))
}
