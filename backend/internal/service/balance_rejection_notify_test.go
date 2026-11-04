//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type rejectedBalanceRecorder struct{ calls chan float64 }

func (r *rejectedBalanceRecorder) CheckBalanceOnRejection(_ *User, balance float64) {
	r.calls <- balance
}

func TestBalanceRejectionNotifiesSharedBillingBoundary(t *testing.T) {
	recorder := &rejectedBalanceRecorder{calls: make(chan float64, 1)}
	svc := NewBillingCacheService(&balanceEligibilityCacheStub{balance: -0.25}, nil, nil, nil, nil, nil, &config.Config{}, nil)
	defer svc.Stop()
	svc.SetBalanceRejectionNotifier(recorder)
	require.ErrorIs(t, svc.CheckBillingEligibility(context.Background(), &User{ID: 71, Balance: 99}, nil, nil, nil, ""), ErrInsufficientBalance)
	select {
	case balance := <-recorder.calls:
		require.Equal(t, -0.25, balance)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("balance rejection returned without scheduling a notification")
	}
}

type exhaustedSubscriptionCache struct{ balanceEligibilityCacheStub }

func (*exhaustedSubscriptionCache) GetSubscriptionCache(context.Context, int64, int64) (*SubscriptionCacheData, error) {
	return &SubscriptionCacheData{Status: SubscriptionStatusActive, ExpiresAt: time.Now().Add(time.Hour), DailyUsage: 10}, nil
}
func TestBalanceRejectionDoesNotNotifySubscriptionQuota(t *testing.T) {
	recorder := &rejectedBalanceRecorder{calls: make(chan float64, 1)}
	svc := &BillingCacheService{cache: &exhaustedSubscriptionCache{}, cfg: &config.Config{}}
	svc.SetBalanceRejectionNotifier(recorder)
	limit := 1.0
	group := &Group{ID: 3, SubscriptionType: SubscriptionTypeSubscription, DailyLimitUSD: &limit}
	err := svc.CheckBillingEligibility(context.Background(), &User{ID: 71, Balance: -1}, nil, group, &UserSubscription{}, "")
	require.ErrorIs(t, err, ErrDailyLimitExceeded)
	require.Empty(t, recorder.calls)
}
func TestBalanceRejectionProviderWiresBothBoundaries(t *testing.T) {
	notify, _, _, _ := balanceRejectionFixture(t)
	svc := ProvideBillingCacheService(nil, nil, nil, nil, nil, nil, &config.Config{}, nil, notify)
	defer svc.Stop()
	api, err := ProvideAPIKeyService(nil, nil, nil, nil, nil, nil, &config.Config{}, svc, nil, nil, nil)
	require.NoError(t, err)
	require.Same(t, notify, svc.balanceRejectionNotifier)
	require.Same(t, notify, api.balanceRejectionNotifier)
}
