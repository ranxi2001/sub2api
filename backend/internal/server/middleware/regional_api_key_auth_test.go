package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type regionalAuthStub struct{ err error }

func (s regionalAuthStub) GetRegionalByKey(_ context.Context, key string) (*service.APIKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	if key != "valid" {
		return nil, service.ErrAPIKeyNotFound
	}
	return &service.APIKey{ID: 1, UserID: 2, User: &service.User{ID: 2, Status: service.StatusActive}, IPWhitelist: []string{"127.0.0.1"}, CompiledIPWhitelist: ip.CompileIPRules([]string{"127.0.0.1"})}, nil
}
func TestRegionalAuthMiddlewareLocalIdentityAndIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, header, key, path, remote string
		err                             error
		status                          int
	}{
		{name: "bearer", header: "Authorization", key: "Bearer valid", status: 204},
		{name: "anthropic", header: "x-api-key", key: "valid", status: 204},
		{name: "google", header: "x-goog-api-key", key: "valid", status: 204},
		{name: "missing", status: 401},
		{name: "unknown", header: "x-api-key", key: "wrong", status: 401},
		{name: "revoked", header: "x-api-key", key: "valid", err: service.ErrAPIKeyNotFound, status: 401},
		{name: "expired-view", header: "x-api-key", key: "valid", err: service.ErrRegionalAuthUnavailable, status: 503},
		{name: "ip-denied", header: "x-api-key", key: "valid", remote: "192.0.2.1:1234", status: 403},
		{name: "query-key", path: "/?key=valid", status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.Use(RegionalAPIKeyAuth(regionalAuthStub{err: tc.err}, &config.Config{}))
			router.GET("/", func(c *gin.Context) {
				key, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				require.Equal(t, int64(1), key.ID)
				c.Status(204)
			})
			path := tc.path
			if path == "" {
				path = "/"
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "127.0.0.1:1234"
			if tc.remote != "" {
				req.RemoteAddr = tc.remote
			}
			if tc.header != "" {
				req.Header.Set(tc.header, tc.key)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
		})
	}
}

func TestRegionalAuthRejectsUsageRecordsBeforeReadingSnapshot(t *testing.T) {
	called := false
	auth := regionalAuthStub{}
	r := gin.New()
	r.Use(RegionalAPIKeyAuth(regionalAuthAuthenticatorFunc(func(context.Context, string) (*service.APIKey, error) {
		called = true
		return auth.GetRegionalByKey(context.Background(), "valid")
	}), &config.Config{}))
	r.GET("/v1/usage/records", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodGet, "/v1/usage/records?start_time=0&end_time=1", nil)
	req.Header.Set("Authorization", "Bearer valid")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "primary site")
	require.False(t, called, "regional usage records must not consult the potentially stale snapshot")
}

type regionalAuthAuthenticatorFunc func(context.Context, string) (*service.APIKey, error)

func (f regionalAuthAuthenticatorFunc) GetRegionalByKey(ctx context.Context, key string) (*service.APIKey, error) {
	return f(ctx, key)
}

type regionalRevocationStub struct {
	calls int
	limit int
}

func (s *regionalRevocationStub) GetRegionalByKey(context.Context, string) (*service.APIKey, error) {
	s.calls++
	if s.calls > 1 {
		return nil, service.ErrAPIKeyNotFound
	}
	return &service.APIKey{ID: 1, UserID: 2, Status: service.StatusAPIKeyActive, ConcurrencyLimit: s.limit, User: &service.User{ID: 2, Status: service.StatusActive}}, nil
}
func TestRegionalAuthInstallsLocalTurnRevalidationAndAdmissionOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		method, path string
		limit        int
	}{{http.MethodGet, "/v1/responses", 0}, {http.MethodPost, "/v1/responses", 2}} {
		t.Run(tc.method, func(t *testing.T) {
			auth := &regionalRevocationStub{limit: tc.limit}
			r := gin.New()
			r.Use(RegionalAPIKeyAuth(auth, &config.Config{}))
			r.Handle(tc.method, tc.path, func(c *gin.Context) {
				require.True(t, service.HasAPIKeyAdmissionOwner(c.Request.Context()))
				require.True(t, service.HasAPIKeyQueueAuthRevalidator(c.Request.Context()))
				_, installed, err := (&service.ConcurrencyService{}).RevalidateAPIKeyQueueTurn(c.Request.Context())
				require.True(t, installed)
				require.Error(t, err)
				require.Equal(t, 2, auth.calls)
				c.Status(http.StatusNoContent)
			})
			request := httptest.NewRequest(tc.method, tc.path, nil)
			request.Header.Set("Authorization", "Bearer fixture")
			response := httptest.NewRecorder()
			r.ServeHTTP(response, request)
			require.Equal(t, http.StatusNoContent, response.Code)
		})
	}
}
