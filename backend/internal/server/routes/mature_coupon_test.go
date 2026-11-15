package routes

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMatureCouponRoutesRequireAuthentication(t *testing.T) {
	r := gin.New()
	deny := func(c *gin.Context) { c.AbortWithStatus(401) }
	noop := func(c *gin.Context) { c.Next() }
	RegisterPaymentRoutes(r.Group("/api/v1"), &handler.PaymentHandler{}, &handler.PaymentWebhookHandler{}, &admin.PaymentHandler{}, middleware.JWTAuthMiddleware(deny), middleware.AdminAuthMiddleware(deny), middleware.AuditLogMiddleware(noop), nil, nil, nil)
	for _, path := range []string{"/payment/coupons", "/payment/coupons/usage", "/admin/payment/coupons/templates", "/admin/payment/coupons/usage"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1"+path, nil))
		require.Equal(t, 401, w.Code, "coupon rewards must have authenticated management and owner-only query APIs")
	}
}
