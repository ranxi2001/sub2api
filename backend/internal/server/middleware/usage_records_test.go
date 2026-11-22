//go:build unit

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUsageRecordsReadMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(CORS(config.CORSConfig{AllowedOrigins: []string{"https://client.example"}}))
	r.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI}})
	})
	r.Use(GroupAntiDistillation(func(context.Context, *service.APIKey) (time.Duration, error) {
		t.Fatal("a usage query must not consult generation admission")
		return time.Minute, nil
	}))
	r.GET("/v1/usage/records", func(c *gin.Context) { c.Status(204) })
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		req := httptest.NewRequest(method, "/v1/usage/records", nil)
		req.Header.Set("Origin", "https://client.example")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, 204, w.Code)
		for _, name := range []string{"X-Next-Cursor", "X-Has-More", "Retry-After", "X-Sub2api-Usage-Id", "X-Request-Id"} {
			require.Contains(t, w.Header().Get("Access-Control-Expose-Headers"), name)
		}
		require.Contains(t, w.Header().Get("Access-Control-Allow-Headers"), "X-Goog-API-Key")
	}
}

func TestUsageRecordsUnknownKeysUseInvalidAuthAbuseGuard(t *testing.T) {
	var repoCalls int
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
		repoCalls++
		return nil, service.ErrAPIKeyNotFound
	}}
	cfg := invalidAuthAbuseTestConfig(10)
	cfg.APIKeyAuth.L1Size = 100
	cfg.APIKeyAuth.NegativeTTLSeconds = 30
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	r := gin.New()
	r.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(svc, nil, cfg)))
	r.GET("/v1/usage/records", func(c *gin.Context) { t.Fatal("unknown key passed authentication") })
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/usage/records?start_time=0&end_time=1", nil)
		req.RemoteAddr = "8.8.8.8:1234"
		req.Header.Set("Authorization", "Bearer random-"+strconv.Itoa(i))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/usage/records?start_time=0&end_time=1", nil)
	req.RemoteAddr = "8.8.8.8:1234"
	req.Header.Set("Authorization", "Bearer random-blocked")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_AUTH_RATE_LIMITED")
	require.Equal(t, 11, repoCalls, "one ordinary cold lookup per credential and zero additional authoritative lookups")

	// Ristretto admits L1 entries asynchronously. Wait for a confirmed negative hit,
	// then repeat the actual request and verify that neither lookup touches the repo.
	require.Eventually(t, func() bool {
		before := repoCalls
		_, err := svc.GetByKey(context.Background(), "random-blocked")
		return errors.Is(err, service.ErrAPIKeyNotFound) && before == repoCalls
	}, time.Second, time.Millisecond)
	before := repoCalls
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req.Clone(context.Background()))
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, before, repoCalls, "a cached miss must not reach authoritative revalidation")
}
