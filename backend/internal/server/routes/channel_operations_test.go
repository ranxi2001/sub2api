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

func TestChannelOperationsRoutesRequireExistingAdministratorAuthentication(t *testing.T) {
	router := gin.New()
	group := router.Group("/api/v1/admin")
	group.Use(gin.HandlerFunc(middleware.NewAdminAuthMiddleware(nil, nil, nil, nil)))
	// Exercise actual route registration (including static/wildcard coexistence).
	registerChannelRoutes(group, &handler.Handlers{Admin: &handler.AdminHandlers{Channel: admin.NewChannelHandler(nil, nil, nil)}})
	for _, tc := range []struct{ method, path string }{
		{"GET", "/profit-operations/snapshot"}, {"GET", "/upstream-operations/snapshot"}, {"GET", "/channels/7/operations"}, {"POST", "/channels/7/operations"},
		{"GET", "/channels/operations/snapshot"}, {"POST", "/channels"},
		{"PUT", "/channels/7"}, {"DELETE", "/channels/7"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/v1/admin"+tc.path, nil))
			require.Equal(t, 401, w.Code)
		})
	}
}

func TestChannelOperationsRoutesDenyScopedObserverManagement(t *testing.T) {
	router := gin.New()
	group := router.Group("/api/v1/admin")
	group.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithObserverScope(c.Request.Context(), []int64{11}))
	})
	group.Use((&admin.AccountHandler{}).AuthorizeObserver)
	registerChannelRoutes(group, &handler.Handlers{Admin: &handler.AdminHandlers{Channel: admin.NewChannelHandler(nil, nil, nil)}})
	for _, tc := range []struct{ method, path string }{{"GET", "/profit-operations/snapshot"}, {"GET", "/upstream-operations/snapshot"}, {"GET", "/channels/7/operations"}, {"POST", "/channels/7/operations"}, {"GET", "/channels/operations/snapshot"}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/v1/admin"+tc.path, nil))
		require.Equal(t, 403, w.Code)
	}
}
