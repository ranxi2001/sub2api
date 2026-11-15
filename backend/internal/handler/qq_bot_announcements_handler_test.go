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

func TestQQBotAnnouncementRequiresAdminAndStrictBody(t *testing.T) {
	for _, tc := range []struct {
		role, body string
		want       int
	}{
		{"", `{}`, http.StatusForbidden},
		{"user", `{}`, http.StatusForbidden},
		{"admin", `{"unknown":true}`, http.StatusBadRequest},
		{"admin", `{} {}`, http.StatusBadRequest},
		{"admin", `{}`, http.StatusBadRequest},
		{"admin", `{"text":"` + strings.Repeat("a", 33<<10) + `"}`, http.StatusBadRequest},
	} {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), tc.role); c.Next() })
		h := NewQQBotHandler(qqbot.New(nil, nil, false, nil))
		r.POST("/announcements", h.SendAnnouncement)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/announcements", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, tc.want, w.Code, w.Body.String())
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	}
}
