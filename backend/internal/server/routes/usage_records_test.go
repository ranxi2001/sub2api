//go:build unit

package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	ratelimit "github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type recordsRouteKeys struct {
	keyBillingRouteAPIKeyRepo
	lookups int
}

func (r *recordsRouteKeys) GetByKeyForAuth(ctx context.Context, credential string) (*service.APIKey, error) {
	r.lookups++
	return r.keyBillingRouteAPIKeyRepo.GetByKeyForAuth(ctx, credential)
}

func (*recordsRouteKeys) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

type recordsRouteSubscriptions struct {
	service.UserSubscriptionRepository
}

func (recordsRouteSubscriptions) GetActiveByUserIDAndGroupID(_ context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	return &service.UserSubscription{UserID: userID, GroupID: groupID, Status: service.StatusActive}, nil
}

type recordsRouteLogs struct {
	service.UsageLogRepository
	userID, keyID int64
}

func (r *recordsRouteLogs) ListUsageRecords(_ context.Context, userID, keyID int64, _, _ time.Time, _ *service.UsageRecordPosition, _ int) ([]service.UsageLog, error) {
	r.userID, r.keyID = userID, keyID
	return nil, nil
}

func registerRecordsRoute(t *testing.T, r *gin.Engine, keys *service.APIKeyService, subs *service.SubscriptionService, settings *service.SettingService, cfg *config.Config) *recordsRouteLogs {
	t.Helper()
	logs := &recordsRouteLogs{}
	usage := handler.NewUsageHandler(service.NewUsageService(logs, nil, nil, nil), keys, nil, nil)
	client := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = client.Close() })
	h := &handler.Handlers{
		Gateway: &handler.GatewayHandler{}, OpenAIGateway: &handler.OpenAIGatewayHandler{},
		UsageRecords: handler.NewUsageRecordsHandler(usage, ratelimit.NewRateLimiter(client), cfg),
	}
	RegisterGatewayRoutes(r, h, middleware.NewAPIKeyAuthMiddleware(keys, subs, cfg), keys, subs, nil, settings, nil, cfg)
	return logs
}

// Exercise actual route ordering, authentication and regional discovery. A multi-group
// key has no group resolver in the fixture, so generation group selection cannot succeed.
func TestGatewayUsageRecordsReadOnlyAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, status, header string
		code                 int
	}{
		{"empty balance", service.StatusActive, "Authorization", 200},
		{"expired", service.StatusAPIKeyExpired, "x-api-key", 200},
		{"exhausted", service.StatusAPIKeyQuotaExhausted, "x-goog-api-key", 200},
		{"disabled", service.StatusAPIKeyDisabled, "x-api-key", 401},
		{"missing key", service.StatusActive, "", 401},
		{"unknown key", service.StatusActive, "unknown-key", 401},
		{"deleted after warmup", service.StatusActive, "x-api-key", 401},
		{"disabled after warmup", service.StatusActive, "x-api-key", 401},
		{"owner changed after warmup", service.StatusActive, "x-api-key", 401},
		{"key replaced after warmup", service.StatusActive, "x-api-key", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{RunMode: config.RunModeStandard, JWT: config.JWTConfig{Secret: "local-test"}}
			cfg.Runtime.RegionalFundsFile = "test-discovery"
			cfg.APIKeyAuth = config.APIKeyAuthCacheConfig{L1Size: 100, L1TTLSeconds: 60}
			group := &service.Group{ID: 6, Status: service.StatusActive, Platform: service.PlatformOpenAI, Hydrated: true}
			user := &service.User{ID: 7, Status: service.StatusActive, Role: service.RoleUser}
			past := time.Now().Add(-time.Hour)
			key := &service.APIKey{ID: 5, UserID: user.ID, User: user, Key: "local-records-key", Status: tc.status,
				Group: group, GroupID: &group.ID, GroupIDs: []int64{group.ID, 8}, Quota: 1, QuotaUsed: 1, ExpiresAt: &past}
			repo := &recordsRouteKeys{keyBillingRouteAPIKeyRepo: keyBillingRouteAPIKeyRepo{apiKey: key}}
			keys := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
			r := gin.New()
			logs := registerRecordsRoute(t, r, keys, nil, nil, cfg)
			req := httptest.NewRequest(http.MethodGet, "/v1/usage/records?start_time=0&end_time=1&api_key_id=88&user_id=99", nil)
			switch tc.header {
			case "Authorization":
				req.Header.Set(tc.header, "Bearer "+key.Key)
			case "unknown-key":
				req.Header.Set("Authorization", "Bearer deleted")
			case "":
			default:
				req.Header.Set(tc.header, key.Key)
			}
			if strings.HasSuffix(tc.name, "after warmup") {
				// Exercise the ordinary cached authentication path with the same credential.
				warm := gin.New()
				warm.GET("/v1/usage", gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)), func(c *gin.Context) { c.Status(204) })
				require.Eventually(t, func() bool {
					before := repo.lookups
					w := httptest.NewRecorder()
					warmReq := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
					warmReq.Header.Set(tc.header, key.Key)
					warm.ServeHTTP(w, warmReq)
					return w.Code == 204 && repo.lookups == before
				}, time.Second, time.Millisecond)
				w := httptest.NewRecorder()
				before := repo.lookups
				r.ServeHTTP(w, req.Clone(context.Background()))
				require.Equal(t, 200, w.Code)
				require.Equal(t, before+1, repo.lookups, "one authoritative lookup after a cache hit")
				logs.keyID = 0
				// Another instance changes the credential without notifying this instance's L1.
				switch tc.name {
				case "deleted after warmup":
					repo.apiKey = nil
				case "disabled after warmup":
					repo.apiKey.Status = service.StatusAPIKeyDisabled
				case "owner changed after warmup":
					repo.apiKey.UserID = 42
					repo.apiKey.User = &service.User{ID: 42, Status: service.StatusActive, Role: service.RoleUser}
				case "key replaced after warmup":
					repo.apiKey.ID++
				}
				stale, err := keys.GetByKey(context.Background(), key.Key)
				require.NoError(t, err)
				require.Equal(t, service.StatusActive, stale.Status)
				require.Equal(t, int64(5), stale.ID)
				require.Equal(t, user.ID, stale.UserID)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tc.code, w.Code, w.Body.String())
			if tc.code == 200 {
				require.Equal(t, user.ID, logs.userID)
				require.Equal(t, key.ID, logs.keyID)
				require.Contains(t, w.Body.String(), `"data":[]`)
			} else {
				require.Zero(t, logs.keyID)
				if strings.HasSuffix(tc.name, "after warmup") {
					wantBody := `{"code":"INVALID_API_KEY","message":"Invalid API key"}`
					if tc.name == "disabled after warmup" {
						wantBody = `{"code":"API_KEY_DISABLED","message":"API key is disabled"}`
					}
					require.JSONEq(t, wantBody, w.Body.String())
				}
			}
		})
	}
}

func TestGatewayUsageRecordsAuthoritativeAdmission(t *testing.T) {
	newKey := func() *service.APIKey {
		group := &service.Group{ID: 6, Hydrated: true, Platform: service.PlatformOpenAI, Status: service.StatusActive}
		user := &service.User{ID: 7, Status: service.StatusActive, Role: service.RoleAdmin, Concurrency: 1}
		return &service.APIKey{ID: 5, Key: "local-records-key", Name: "cached", Status: service.StatusActive, UserID: user.ID, User: user, GroupID: &group.ID, Group: group}
	}
	unassign := func(k *service.APIKey) { k.GroupID, k.Group = nil, nil }
	restrict := func(k *service.APIKey) { k.Group.CodexCLIOnly = true }
	subscribe := func(k *service.APIKey) { k.Group.SubscriptionType = service.SubscriptionTypeSubscription }
	rebindSubscription := func(k *service.APIKey) {
		subscribe(k)
		k.Group.ID = 12
		k.GroupID = &k.Group.ID
	}
	denyIP := func(k *service.APIKey) { k.IPBlacklist = []string{"192.0.2.1"} }
	disable := func(k *service.APIKey) { k.Status = service.StatusAPIKeyDisabled }
	for _, tc := range []struct {
		name            string
		cached, current func(*service.APIKey)
		allowUngrouped  bool
		simpleMode      bool
		rejection       middleware.IngressRejectReason
	}{
		{name: "unassigned while ungrouped keys forbidden", current: unassign, rejection: middleware.IngressRejectGroupUnassigned},
		{name: "Codex restriction enabled", current: restrict, rejection: middleware.IngressRejectCodexClientRequired},
		{name: "Codex restriction lifted", cached: restrict},
		{name: "subscription group assigned", current: subscribe},
		{name: "subscription group changed", cached: subscribe, current: rebindSubscription},
		{name: "subscription group removed while ungrouped keys allowed", cached: subscribe, current: unassign, allowUngrouped: true},
		{name: "simple mode uses current identity", cached: restrict, current: subscribe, simpleMode: true},
		{name: "client IP newly denied", current: denyIP, rejection: middleware.IngressRejectIPRestricted},
		{name: "client IP allowed again", cached: denyIP},
		{name: "key enabled again", cached: disable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cached, current := newKey(), newKey()
			if tc.cached != nil {
				tc.cached(cached)
			}
			if tc.current != nil {
				tc.current(current)
			}
			current.Name, current.User.Role, current.User.Concurrency = "current", service.RoleUser, 8
			current.ConcurrencyLimit = 2
			cfg := &config.Config{RunMode: config.RunModeStandard, APIKeyAuth: config.APIKeyAuthCacheConfig{L1Size: 100, L1TTLSeconds: 60}}
			if tc.simpleMode {
				cfg.RunMode = config.RunModeSimple
			}
			repo := &recordsRouteKeys{keyBillingRouteAPIKeyRepo: keyBillingRouteAPIKeyRepo{apiKey: cached}}
			keys := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
			settings := service.NewSettingService(&channelMonitorRouteSettingRepoStub{values: map[string]string{
				service.SettingKeyAllowUngroupedKeyScheduling: strconv.FormatBool(tc.allowUngrouped),
			}}, cfg)
			subs := service.NewSubscriptionService(nil, recordsRouteSubscriptions{}, nil, nil, cfg)
			t.Cleanup(subs.Stop)
			r := gin.New()
			var observed *gin.Context
			r.Use(func(c *gin.Context) { c.Next(); observed = c.Copy() })
			logs := registerRecordsRoute(t, r, keys, subs, settings, cfg)

			require.Eventually(t, func() bool {
				before := repo.lookups
				_, err := keys.GetByKey(context.Background(), cached.Key)
				return err == nil && repo.lookups == before
			}, time.Second, time.Millisecond)
			repo.apiKey = current
			before := repo.lookups
			stale, err := keys.GetByKey(context.Background(), cached.Key)
			require.NoError(t, err)
			require.Equal(t, cached.Name, stale.Name)
			require.Equal(t, before, repo.lookups)

			req := httptest.NewRequest(http.MethodGet, "/v1/usage/records?start_time=0&end_time=1", nil)
			req.RemoteAddr = "192.0.2.1:1234"
			req.Header.Set("x-api-key", current.Key)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, before+1, repo.lookups, "exactly one primary lookup after the L1 hit")
			fallback, ok := middleware.GetOpsFallbackAPIKey(observed)
			require.True(t, ok)
			require.Equal(t, current.Name, fallback.Name)
			if tc.rejection != "" {
				require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
				reason, rejected := middleware.GetIngressRejectReason(observed)
				require.True(t, rejected)
				require.Equal(t, tc.rejection, reason)
				require.Zero(t, logs.keyID)
				return
			}
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, current.ID, logs.keyID)
			require.Equal(t, current.UserID, logs.userID)
			key, ok := middleware.GetAPIKeyFromContext(observed)
			require.True(t, ok)
			require.Same(t, fallback, key)
			subject, ok := middleware.GetAuthSubjectFromContext(observed)
			require.True(t, ok)
			require.Equal(t, middleware.AuthSubject{UserID: current.UserID, Concurrency: current.User.Concurrency}, subject)
			role, ok := middleware.GetUserRoleFromContext(observed)
			require.True(t, ok)
			require.Equal(t, current.User.Role, role)
			ctx := observed.Request.Context()
			require.Equal(t, current.UserID, ctx.Value(ctxkey.UserID))
			require.True(t, service.HasAPIKeyQueueAuthRevalidator(ctx))
			group, _ := ctx.Value(ctxkey.Group).(*service.Group)
			require.Equal(t, current.Group, group)
			subscription, hasSubscription := middleware.GetSubscriptionFromContext(observed)
			require.Equal(t, !tc.simpleMode && current.Group != nil && current.Group.IsSubscriptionType(), hasSubscription)
			if hasSubscription {
				require.Equal(t, current.Group.ID, subscription.GroupID)
				require.Equal(t, current.UserID, subscription.UserID)
			}
		})
	}
}
