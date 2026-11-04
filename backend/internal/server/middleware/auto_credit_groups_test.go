//go:build unit

package middleware

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

type creditFallbackGroups struct {
	service.GroupRepository
	groups map[int64]*service.Group
}

func (r creditFallbackGroups) GetByIDLite(_ context.Context, id int64) (*service.Group, error) {
	return r.groups[id], nil
}

type creditFallbackRouter struct{}

func (creditFallbackRouter) ProbeAPIKeyGroup(context.Context, *service.Group, string, string) (service.APIKeyGroupAvailability, error) {
	return service.APIKeyGroupAvailability{Available: true}, nil
}

type creditFallbackSubscriptions struct {
	service.UserSubscriptionRepository
}

func (creditFallbackSubscriptions) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*service.UserSubscription, error) {
	return &service.UserSubscription{ID: 30, Status: service.StatusActive, ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func TestVipCreditNoConfigPreservesSubscriptionGroupFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	id := int64(1)
	groups := map[int64]*service.Group{1: {ID: 1, Status: service.StatusActive, RateMultiplier: 0.1, Platform: service.PlatformOpenAI}, 2: {ID: 2, Status: service.StatusActive, RateMultiplier: 1, Platform: service.PlatformOpenAI, SubscriptionType: service.SubscriptionTypeSubscription}}
	key := &service.APIKey{ID: 7, UserID: 8, Status: service.StatusActive, GroupID: &id, GroupIDs: []int64{1, 2}, Group: groups[1], User: &service.User{ID: 8, Status: service.StatusActive, Balance: 0}}
	svc := service.NewAPIKeyService(nil, nil, creditFallbackGroups{groups: groups}, creditFallbackSubscriptions{}, nil, nil, &config.Config{})
	svc.SetGroupRouter(creditFallbackRouter{})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	got, err := selectAPIKeyRequestGroup(c, svc, nil, &config.Config{}, key, false)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, int64(2), *got.GroupID, "zero-balance non-VIP must fall back to valid subscription rather than cheap unusable balance group")
}

func TestVipCreditBelowReservePreservesSubscriptionFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	id := int64(1)
	groups := map[int64]*service.Group{1: {ID: 1, Status: service.StatusActive, RateMultiplier: 0.1, Platform: service.PlatformOpenAI}, 2: {ID: 2, Status: service.StatusActive, RateMultiplier: 1, Platform: service.PlatformOpenAI, SubscriptionType: service.SubscriptionTypeSubscription}}
	cfg := &config.Config{}
	cfg.Billing.MinimumBalanceReserve = 0.01
	key := &service.APIKey{ID: 7, UserID: 8, Status: service.StatusActive, GroupID: &id, GroupIDs: []int64{1, 2}, Group: groups[1], User: &service.User{ID: 8, Status: service.StatusActive, Balance: 0.005}}
	svc := service.NewAPIKeyService(nil, nil, creditFallbackGroups{groups: groups}, creditFallbackSubscriptions{}, nil, nil, cfg)
	svc.SetGroupRouter(creditFallbackRouter{})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	got, err := selectAPIKeyRequestGroup(c, svc, nil, cfg, key, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), *got.GroupID)
}
