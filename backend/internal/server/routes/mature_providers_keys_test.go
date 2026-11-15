package routes

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMatureProvidersKeyCreateConfirmation(t *testing.T) {
	r := gin.New()
	registerChannelRoutes(r.Group("/api/v1/admin"), &handler.Handlers{Admin: &handler.AdminHandlers{Channel: admin.NewChannelHandler(nil, nil, nil)}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/admin/providers/1/keys", strings.NewReader(`{"group_id":"4","revision":1}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, 400, w.Code, "remote key creation requires explicit confirmation before any dependency or network access")
}
