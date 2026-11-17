package routes

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProfitOperationsRoutesRequireExistingAdministratorAuthentication(t *testing.T) {
	router := gin.New()
	group := router.Group("/api/v1/admin")
	group.Use(gin.HandlerFunc(middleware.NewAdminAuthMiddleware(nil, nil, nil, nil)))
	// Exercise actual route registration (including static/wildcard coexistence).
	registerChannelRoutes(group, &handler.Handlers{Admin: &handler.AdminHandlers{Channel: admin.NewChannelHandler(nil, nil, nil)}})
	for _, tc := range []struct{ method, path string }{{"GET", "/profit-operations/snapshot"}, {"GET", "/profit-operations/providers"}, {"PUT", "/providers/1/profit-channel"}, {"POST", "/providers/1/profit-charges/sync"}, {"GET", "/providers/1/profit-charges/sync/test-job"}, {"GET", "/upstream-channels"}, {"POST", "/upstream-channels"}, {"GET", "/upstream-channels/1"}, {"PUT", "/upstream-channels/1"}, {"DELETE", "/upstream-channels/1"}, {"GET", "/upstream-channels/1/members"}, {"GET", "/upstream-channel-bindings"}, {"PUT", "/upstream-channel-bindings"}} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/v1/admin"+tc.path, nil))
			require.Equal(t, 401, w.Code)
		})
	}
}

func TestProfitOperationsRoutesDenyScopedObserverManagement(t *testing.T) {
	router := gin.New()
	group := router.Group("/api/v1/admin")
	group.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithObserverScope(c.Request.Context(), []int64{11}))
	})
	group.Use((&admin.AccountHandler{}).AuthorizeObserver)
	registerChannelRoutes(group, &handler.Handlers{Admin: &handler.AdminHandlers{Channel: admin.NewChannelHandler(nil, nil, nil)}})
	for _, tc := range []struct{ method, path string }{{"GET", "/profit-operations/snapshot"}, {"GET", "/profit-operations/providers"}, {"PUT", "/providers/1/profit-channel"}, {"POST", "/providers/1/profit-charges/sync"}, {"GET", "/providers/1/profit-charges/sync/test-job"}, {"GET", "/upstream-channels"}, {"POST", "/upstream-channels"}, {"GET", "/upstream-channels/1"}, {"PUT", "/upstream-channels/1"}, {"DELETE", "/upstream-channels/1"}, {"GET", "/upstream-channels/1/members"}, {"GET", "/upstream-channel-bindings"}, {"PUT", "/upstream-channel-bindings"}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/v1/admin"+tc.path, nil))
		require.Equal(t, 403, w.Code)
	}
}
