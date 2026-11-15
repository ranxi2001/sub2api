package routes

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Missing feature must fail at an existing HTTP boundary, never at compilation.
func TestMatureQQBotWebhookSafeDefault(t *testing.T) {
	r := gin.New()
	noop := func(c *gin.Context) { c.Next() }
	RegisterAuthRoutes(r.Group("/api/v1"), &handler.Handlers{}, middleware.JWTAuthMiddleware(noop), middleware.AuditLogMiddleware(noop), nil, nil, nil)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"unconfigured", `{"op":0,"id":"synthetic-event"}`, 503},
		{"bounded", strings.Repeat("x", (2<<20)+1), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/v1/qqbot/webhook", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, "QQ callback must bound input and fail closed when unconfigured")
		})
	}
}
