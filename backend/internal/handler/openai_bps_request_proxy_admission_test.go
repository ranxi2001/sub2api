//go:build unit && bps_request_integration

// The integration tag keeps this new-API fixture out of historical unit RED
// overlays; account-request-proxy.yml runs both admission backends explicitly.
package handler

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Both handlers have independent ConcurrencyService/cache/Redis clients. The
// shared account occupancy is the production cache's Lua authority, never a
// test counter or a per-proxy limit. This stops at handler admission; forwarding
// and actual proxy distribution are covered by the transport acceptance tests.
func TestBPSRequestProxyAdmissionRetainsAccountConcurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("production_cache_with_miniredis", func(t *testing.T) {
		server := miniredis.RunT(t)
		testBPSRequestProxyAccountAdmission(t, server.Addr(), false)
	})
	t.Run("production_cache_with_real_redis", func(t *testing.T) {
		addr := os.Getenv("BPS_REQUEST_PROXY_REDIS_ADDR")
		if addr == "" {
			t.Skip("set BPS_REQUEST_PROXY_REDIS_ADDR to an explicitly disposable loopback Redis for real-server evidence")
		}
		host, _, err := net.SplitHostPort(addr)
		require.NoError(t, err)
		require.Contains(t, []string{"127.0.0.1", "::1", "localhost"}, host, "the integration fixture must never address a non-loopback Redis")
		testBPSRequestProxyAccountAdmission(t, addr, true)
	})
}

func testBPSRequestProxyAccountAdmission(t *testing.T, addr string, realRedis bool) {
	t.Helper()
	var caches [2]service.ConcurrencyCache
	var handlers [2]*OpenAIGatewayHandler
	var connectionIDs [2]int64
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for node := range handlers {
		// No application configuration, credentials, or FlushDB is used. Each
		// case has a fresh positive account ID and releases only its own slots.
		client := redis.NewClient(&redis.Options{Addr: addr, DB: 15, PoolSize: 1, MaxRetries: -1, ContextTimeoutEnabled: true})
		t.Cleanup(func() { require.NoError(t, client.Close()) })
		require.NoError(t, client.Ping(ctx).Err())
		if realRedis {
			info, err := client.Info(ctx, "server").Result()
			require.NoError(t, err)
			require.True(t, strings.Contains(info, "redis_version:"), "the real-server case must not silently use miniredis")
			connectionIDs[node], err = client.ClientID(ctx).Result()
			require.NoError(t, err)
		}
		caches[node] = repository.NewConcurrencyCache(client, 1, 60)
		handlers[node] = &OpenAIGatewayHandler{
			gatewayService:    &service.OpenAIGatewayService{},
			concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(caches[node]), SSEPingFormatNone, 0),
		}
	}
	if realRedis {
		require.NotEqual(t, connectionIDs[0], connectionIDs[1], "the shared authority must be reached over independent Redis connections")
	}

	account := &service.Account{
		ID: time.Now().UnixNano(), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Concurrency: 6,
		Extra: map[string]any{
			"openai_excel_bps":           true,
			service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest,
			service.ExcelBPSProxyIDsKey:  []int64{11, 22, 33},
		},
	}
	require.True(t, account.ExcelBPSRequestProxyEnabled(), "the admitted account must have request distribution enabled")

	assertOccupancy := func(want int) {
		t.Helper()
		for node, cache := range caches {
			count, err := cache.GetAccountConcurrency(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, want, count, "handler %d must observe the same account-wide occupancy", node)
		}
	}
	acquire := func(node int, responses bool) (func(), openAISlotAcquireResult, *httptest.ResponseRecorder) {
		t.Helper()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		selection := &service.AccountSelectionResult{
			Account: account,
			WaitPlan: &service.AccountWaitPlan{
				AccountID: account.ID, MaxConcurrency: account.Concurrency,
				Timeout: 10 * time.Millisecond, MaxWaiting: 0,
			},
		}
		streamStarted := false
		if responses {
			release, result := handlers[node].acquireResponsesAccountSlot(c, nil, "", selection, false, &streamStarted, zap.NewNop())
			return release, result, recorder
		}
		release, result := handlers[node].acquireOpenAIAccountSlot(c, nil, "", selection, false, &streamStarted, zap.NewNop(), nil)
		return release, result, recorder
	}

	var releases []func()
	t.Cleanup(func() {
		for _, release := range releases {
			if release != nil {
				release()
			}
		}
	})
	for request := 0; request < 6; request++ {
		release, result, recorder := acquire(request%2, request%2 == 0)
		require.Equal(t, openAISlotAcquireOK, result, "request %d must be admitted: %s", request+1, recorder.Body.String())
		require.NotNil(t, release)
		releases = append(releases, release)
		assertOccupancy(request + 1)
	}

	for _, responses := range []bool{true, false} {
		release, result, recorder := acquire(1, responses)
		require.Equal(t, openAISlotAcquireFailed, result, "a seventh logical request must not gain capacity from the three configured proxies")
		require.Nil(t, release)
		require.Equal(t, http.StatusTooManyRequests, recorder.Code, recorder.Body.String())
		require.Equal(t, gatewayQueueFullCode, gjson.Get(recorder.Body.String(), "error.code").String())
		assertOccupancy(6)
	}

	releases[0]()
	releases[0]() // Handler release is idempotent.
	assertOccupancy(5)
	replacement, result, recorder := acquire(1, true)
	require.Equal(t, openAISlotAcquireOK, result, recorder.Body.String())
	require.NotNil(t, replacement, "the other handler must observe capacity released by the first")
	releases = append(releases, replacement)
	assertOccupancy(6)
	for _, release := range releases {
		release()
	}
	assertOccupancy(0)
}
