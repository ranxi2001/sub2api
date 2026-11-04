package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestVipCreditRoutesDenyNonAdministrators(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/admin")
	g.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 20})
		c.Set(string(middleware.ContextKeyUserRole), service.RoleUser)
		c.Next()
	})
	registerUserManagementRoutes(g, &handler.Handlers{Admin: &handler.AdminHandlers{User: &adminhandler.UserHandler{}, UserAttribute: &adminhandler.UserAttributeHandler{}}})
	for _, x := range []struct{ method, path string }{{"GET", "/admin/users/1/auto-credit"}, {"PUT", "/admin/users/1/auto-credit"}, {"POST", "/admin/users/1/auto-credit/2/settle"}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(x.method, x.path, nil))
		require.Equal(t, 403, w.Code, x.path)
	}
}
