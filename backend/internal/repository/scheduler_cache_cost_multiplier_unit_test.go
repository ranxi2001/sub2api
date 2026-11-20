//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Scoring reads candidate metadata before full-account hydration. Both cache
// payloads must retain procurement cost independently of billing multipliers.
func TestSchedulerCachePreservesCostMultiplier(t *testing.T) {
	for _, accountType := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		t.Run(accountType, func(t *testing.T) {
			ctx := context.Background()
			cache := newSchedulerCacheUnit(t)
			billing, group := 7.0, 3.0
			account := service.Account{
				ID: 9101, Platform: service.PlatformOpenAI, Type: accountType,
				Status: service.StatusActive, Schedulable: true, Concurrency: 10,
				RateMultiplier: &billing, GroupRateMultiplier: &group,
				Extra: map[string]any{service.AccountCostMultiplierExtraKey: 0.14, "unrelated_large_payload": "drop me"},
			}
			bucket := service.SchedulerBucket{GroupID: 17, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
			check := func(want float64) {
				t.Helper()
				candidates, hit, err := cache.GetSnapshot(ctx, bucket)
				require.NoError(t, err)
				require.True(t, hit)
				require.Len(t, candidates, 1)
				require.NotContains(t, candidates[0].Extra, "unrelated_large_payload")
				full, err := cache.GetAccount(ctx, account.ID)
				require.NoError(t, err)
				require.NotNil(t, full)
				for _, cached := range []*service.Account{candidates[0], full} {
					require.Equal(t, want, cached.CostMultiplier())
					require.Equal(t, billing, cached.BillingRateMultiplier())
					require.Equal(t, group, cached.UserGroupRateMultiplier())
				}
			}
			check(0.14)
			// Manual edits and successful probes refresh payloads via SetAccount,
			// without requiring bucket membership to change. Zero is explicit cost.
			for _, cost := range []float64{0.035, 0, 0.25} {
				account.Extra[service.AccountCostMultiplierExtraKey] = cost
				require.NoError(t, cache.SetAccount(ctx, &account))
				check(cost)
			}
			delete(account.Extra, service.AccountCostMultiplierExtraKey)
			require.NoError(t, cache.SetAccount(ctx, &account))
			check(service.DefaultAccountCostMultiplier)
		})
	}
}

func TestSchedulerCachePreservesProbePenalty(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	account := service.Account{ID: 9201, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Extra: map[string]any{service.AccountProbePenaltyExtraKey: map[string]any{"enabled": true, "minimum_charge": .001}}}
	bucket := service.SchedulerBucket{GroupID: 18, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	for _, enabled := range []bool{true, false, true} {
		account.Extra[service.AccountProbePenaltyExtraKey] = map[string]any{"enabled": enabled, "minimum_charge": .001}
		require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(account.Extra))
		require.NoError(t, cache.SetAccount(ctx, &account))
		candidates, hit, err := cache.GetSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.True(t, hit)
		require.Len(t, candidates, 1)
		full, err := cache.GetAccount(ctx, account.ID)
		require.NoError(t, err)
		for _, cached := range []*service.Account{candidates[0], full} {
			if enabled {
				require.Equal(t, .001, cached.ProbePenaltyMinimum())
			} else {
				require.Zero(t, cached.ProbePenaltyMinimum())
			}
		}
	}
}
