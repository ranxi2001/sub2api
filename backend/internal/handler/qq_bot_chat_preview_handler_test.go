package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestQQBotChatPreviewRequiresAdminAndStrictBody(t *testing.T) {
	for _, tc := range []struct {
		role, body string
		want       int
	}{
		{"", `{}`, http.StatusForbidden}, {"user", `{}`, http.StatusForbidden},
		{"admin", `{"group_id":"group","question":"你好","user_id":7}`, http.StatusBadRequest},
		{"admin", `{} {}`, http.StatusBadRequest}, {"admin", `{"group_id":"group","question":"/签到"}`, http.StatusBadRequest},
		{"admin", `{"group_id":"group","question":"` + strings.Repeat("a", 33<<10) + `"}`, http.StatusBadRequest},
	} {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), tc.role); c.Next() })
		h := NewQQBotHandler(qqbot.New(nil, nil, false, nil))
		r.POST("/chat-preview", h.PreviewChat)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/chat-preview", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, tc.want, w.Code, w.Body.String())
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	}
}
