package middleware

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGroupAntiDistillationAdmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, method, path, platform string
		pause                        time.Duration
		err                          error
		status, checks               int
	}{
		{"paused responses", "POST", "/v1/responses", service.PlatformOpenAI, time.Minute, nil, 429, 1},
		{"paused chat", "POST", "/chat/completions", service.PlatformOpenAI, time.Minute, nil, 429, 1},
		{"paused websocket", "GET", "/responses", service.PlatformOpenAI, time.Minute, nil, 429, 1},
		{"disabled or no pause", "POST", "/v1/responses", service.PlatformOpenAI, 0, nil, 204, 1},
		{"cache unavailable", "POST", "/v1/responses", service.PlatformOpenAI, 0, errors.New("unavailable"), 204, 1},
		{"usage remains readable", "GET", "/v1/usage", service.PlatformOpenAI, time.Minute, nil, 204, 0},
		{"models remain readable", "GET", "/v1/models", service.PlatformOpenAI, time.Minute, nil, 204, 0},
		{"media is excluded", "POST", "/v1/images/generations", service.PlatformOpenAI, time.Minute, nil, 204, 0},
		{"other platform", "POST", "/v1/messages", service.PlatformAnthropic, time.Minute, nil, 204, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks, forwarded := 0, false
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set(string(ContextKeyAPIKey), &service.APIKey{UserID: 7, Group: &service.Group{ID: 3, Platform: tc.platform}})
			})
			r.Use(GroupAntiDistillation(func(_ context.Context, key *service.APIKey) (time.Duration, error) {
				checks++
				require.EqualValues(t, 7, key.UserID)
				return tc.pause, tc.err
			}))
			r.Handle(tc.method, tc.path, func(c *gin.Context) { forwarded = true; c.Status(204) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			require.Equal(t, tc.status, w.Code)
			require.Equal(t, tc.checks, checks)
			require.Equal(t, tc.status == 204, forwarded)
			if tc.status == 429 {
				require.Equal(t, "60", w.Header().Get("Retry-After"))
				require.Contains(t, w.Body.String(), "group_anti_distillation_paused")
			}
		})
	}
}
