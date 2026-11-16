//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCacheBPSRequestProxyRoundTrip(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	account := service.Account{ID: 601, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 6, Extra: map[string]any{"openai_excel_bps": true, "openai_excel_bps_proxy_mode": "request", "openai_excel_bps_proxy_ids": []int64{11, 22}, "openai_excel_bps_proxy_source": "ip_pool"}}
	bucket := service.SchedulerBucket{GroupID: 1, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	candidates, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, candidates, 1)
	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	for _, saved := range []*service.Account{candidates[0], full} {
		require.True(t, saved.ExcelBPSRequestProxyEnabled())
		require.Equal(t, []int64{11, 22}, saved.ExcelBPSRequestProxyIDs())
		require.Equal(t, "ip_pool", saved.Extra["openai_excel_bps_proxy_source"])
	}
}

func TestSchedulerCacheOpenAIRequestProxyRoundTrip(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	account := service.Account{ID: 602, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Extra: map[string]any{service.OpenAIProxyModeKey: service.OpenAIProxyModeRequest, service.OpenAIProxyIDsKey: []int64{11, 22}}}
	bucket := service.SchedulerBucket{GroupID: 1, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	candidates, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, candidates, 1)
	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	for _, saved := range []*service.Account{candidates[0], full} {
		require.True(t, saved.OpenAIRequestProxyEnabled())
		require.Equal(t, []int64{11, 22}, saved.OpenAIRequestProxyIDs())
	}
}

func TestSchedulerCacheOpenAIProxyPreservesExplicitNullMode(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	account := service.Account{ID: 603, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Extra: map[string]any{service.OpenAIProxyModeKey: nil, service.OpenAIProxyIDsKey: []int64{11}}}
	bucket := service.SchedulerBucket{GroupID: 1, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	candidates, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, candidates, 1)
	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	for _, saved := range []*service.Account{candidates[0], full} {
		require.True(t, saved.OpenAIRequestProxyEnabled(), "malformed stored mode must not turn into direct fallback")
		require.Empty(t, saved.OpenAIRequestProxyIDs())
	}
}
