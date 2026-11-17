//go:build unit

package routes

import (
	"context"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type usageRateKeyRepo struct {
	service.APIKeyRepository
	calls int
}

func (r *usageRateKeyRepo) ListByUserID(_ context.Context, _ int64, _ pagination.PaginationParams, _ service.APIKeyListFilters) ([]service.APIKey, *pagination.PaginationResult, error) {
	r.calls++
	return nil, &pagination.PaginationResult{}, nil
}

func TestAPIKeyUsageSortSharesHeavyUserBucket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, first := range []string{"/api/v1/keys?sort_by=usage", "/api/v1/usage/invalid"} {
		t.Run(first, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			settings := service.NewSettingService(&scopedPaymentRateSettings{}, nil)
			repo := &usageRateKeyRepo{}
			h := &handler.Handlers{APIKey: handler.NewAPIKeyHandler(service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, nil)), Usage: &handler.UsageHandler{}}
			router := gin.New()
			auth := middleware.JWTAuthMiddleware(func(c *gin.Context) {
				id, _ := strconv.ParseInt(c.GetHeader("Fixture-User"), 10, 64)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: id})
				c.Set(string(middleware.ContextKeyUserRole), c.GetHeader("Fixture-Role"))
				c.Next()
			})
			RegisterUserRoutes(router.Group("/api/v1"), h, auth, middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }), nil, middleware.NewPanelRateLimiter(client, settings))
			request := func(path, user, role string, status, calls int) {
				t.Helper()
				r := httptest.NewRequest("GET", path, nil)
				r.Header.Set("Fixture-User", user)
				r.Header.Set("Fixture-Role", role)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				require.Equal(t, status, w.Code, w.Body.String())
				require.Equal(t, calls, repo.calls, "rate limiting must happen before repository aggregation")
				if status == 429 {
					require.NotEmpty(t, w.Header().Get("Retry-After"))
				}
			}
			calls := 0
			if first == "/api/v1/usage/invalid" {
				request(first, "42", "user", 400, calls)
			} else {
				calls++
				request(first, "42", "user", 200, calls)
			}
			request("/api/v1/keys?sort_by=%20UsAgE%20", "42", "user", 429, calls)
			calls++
			request("/api/v1/keys", "42", "user", 200, calls)
			calls++
			request("/api/v1/keys?sort_by=usage", "43", "user", 200, calls)
			for range 2 {
				calls++
				request("/api/v1/keys?sort_by=usage", "44", "admin", 200, calls)
			}
			require.NoError(t, client.Close())
			calls++
			request("/api/v1/keys?sort_by=usage", "42", "user", 200, calls)
		})
	}
}
