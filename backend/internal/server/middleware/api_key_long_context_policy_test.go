package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyLongContextMiddlewareBlocksEveryTextEntry(t *testing.T) {
	for _, tc := range []struct{ route, path string }{
		{"/v1/messages", "/v1/messages"}, {"/antigravity/v1/messages", "/antigravity/v1/messages"},
		{"/v1/chat/completions", "/v1/chat/completions"}, {"/chat/completions", "/chat/completions"},
		{"/v1/responses", "/v1/responses"}, {"/responses", "/responses"},
		{"/v1/responses/*subpath", "/v1/responses/compact"}, {"/responses/*subpath", "/responses/compact"},
		{"/backend-api/codex/responses", "/backend-api/codex/responses"},
		{"/backend-api/codex/responses/*subpath", "/backend-api/codex/responses/compact"},
		{"/v1beta/models/*modelAction", "/v1beta/models/gemini-2.5-pro:generateContent"},
		{"/antigravity/v1beta/models/*modelAction", "/antigravity/v1beta/models/gemini-2.5-pro:streamGenerateContent"},
		{"/v1/systemone", "/v1/systemone"}, {"/v1beta/cachedContents", "/v1beta/cachedContents"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set(string(ContextKeyAPIKey), &service.APIKey{}); c.Next() })
			router.Use(APIKeyLongContextPolicy(nil))
			called := false
			router.POST(tc.route, func(c *gin.Context) { called = true; c.Status(http.StatusOK) })
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"model":"test","previous_response_id":"resp_fixture","input":"hi"}`))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			require.False(t, called, "blocked request must never reach forwarding")
		})
	}
}

func TestAPIKeyLongContextMiddlewareIsolationBodyAndExemptions(t *testing.T) {
	for _, tc := range []struct {
		path   string
		allow  bool
		checks int
	}{
		{"/v1/responses", false, 1}, {"/v1/responses", true, 0}, {"/v1/responses", false, 1},
		{"/v1/messages/count_tokens", false, 0}, {"/v1/responses/input_tokens", false, 0},
		{"/v1/embeddings", false, 0}, {"/v1/images/generations", false, 0},
		{"/v1/alpha/search", false, 0},
	} {
		t.Run(tc.path, func(t *testing.T) {
			const body = `{"model":"test","input":"hello"}`
			checks := 0
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(ContextKeyAPIKey), &service.APIKey{AllowLongContext: tc.allow})
				c.Next()
			})
			router.Use(APIKeyLongContextPolicy(func(_ context.Context, key *service.APIKey, raw []byte, models ...string) error {
				checks++
				require.False(t, key.AllowLongContext)
				require.Equal(t, body, string(raw))
				require.Equal(t, []string{"test"}, models)
				return nil
			}))
			router.POST(tc.path, func(c *gin.Context) {
				raw, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.Equal(t, body, string(raw))
				c.Status(http.StatusOK)
			})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body)))
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, tc.checks, checks)
		})
	}
}
