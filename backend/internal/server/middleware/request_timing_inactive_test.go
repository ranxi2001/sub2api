//go:build unit

package middleware

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/iprisk"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestRequestTimingUnattributedUserInactiveNeverTriggersAnonymousBan(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1, ContextTimeoutEnabled: true})
	t.Cleanup(func() { _ = client.Close() })
	settings := iprisk.DefaultConfig()
	settings.AnonymousAPI.Enabled = true
	settings.AnonymousAPI.Threshold = 2
	guard := iprisk.New(&anonymousSettingsStore{settings}, client, nil, false)
	t.Cleanup(guard.Stop)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.Use(guard.Middleware())
	upstream := 0
	router.POST("/v1/responses", func(c *gin.Context) {
		require.Nil(t, c.Request.Context().Value(ctxkey.UserID))
		MarkIngressRejected(c, IngressRejectUserInactive)
		AbortWithError(c, 401, "USER_INACTIVE", "User account is not active")
	}, func(c *gin.Context) { upstream++; c.Status(200) })
	for i := 0; i < 13; i++ {
		request := httptest.NewRequest("POST", "/v1/responses", nil)
		request.RemoteAddr = "8.8.8.8:1234"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		require.Equal(t, 401, w.Code)
	}
	require.Zero(t, upstream)
	bans, err := guard.AnonymousBans(context.Background())
	require.NoError(t, err)
	require.Empty(t, bans)
	ban, err := guard.AnonymousFailure(context.Background(), "8.8.8.8", "unknown")
	require.NoError(t, err)
	require.Nil(t, ban, "the 13 USER_INACTIVE observations must not consume even one anonymous failure")
}
