package routes

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// This test deliberately uses only frozen-production symbols. A missing API
// fails with an observable 404, never with a missing-symbol compile failure.
func TestMatureProvidersHTTPContract(t *testing.T) {
	for _, role := range []string{"anonymous", "observer", "admin"} {
		t.Run(role, func(t *testing.T) {
			r := gin.New()
			g := r.Group("/api/v1/admin")
			switch role {
			case "anonymous":
				g.Use(gin.HandlerFunc(middleware.NewAdminAuthMiddleware(nil, nil, nil, nil)))
			case "observer":
				g.Use(func(c *gin.Context) {
					c.Request = c.Request.WithContext(service.WithObserverScope(c.Request.Context(), []int64{1}))
				})
				g.Use((&admin.AccountHandler{}).AuthorizeObserver)
			}
			registerChannelRoutes(g, &handler.Handlers{Admin: &handler.AdminHandlers{Channel: admin.NewChannelHandler(nil, nil, nil)}})
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/v1/admin/providers", strings.NewReader(`{"name":"Invalid site","site_type":"unknown","base_url":"https://example.invalid"}`))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			want := map[string]int{"anonymous": 401, "observer": 403, "admin": 400}[role]
			require.Equal(t, want, w.Code, "provider creation must enforce authentication, observer isolation, and site validation at the real HTTP boundary")
		})
	}
}
