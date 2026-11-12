package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyContextTokenLimitEnforcedWithLongContextAllowed(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages", "/v1beta/models/test:generateContent"} {
		t.Run(path, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(ContextKeyAPIKey), &service.APIKey{AllowLongContext: true, ContextTokenLimit: 42})
			})
			router.Use(APIKeyLongContextPolicy(func(_ context.Context, key *service.APIKey, _ []byte, _ ...string) error {
				require.Equal(t, 42, key.ContextTokenLimit)
				return &service.APIKeyLongContextError{Code: "context_unverifiable", Limit: 42, Reason: "private policy diagnostic"}
			}))
			route := path
			if strings.Contains(path, "/models/") {
				route = "/v1beta/models/*modelAction"
			}
			router.POST(route, func(c *gin.Context) { t.Error("request reached forwarding handler") })
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"test","input":"hello"}`)))
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), service.APIKeyContextLengthExceededMessage)
			for _, hidden := range []string{"context_unverifiable", "disabled", "API key", "42", "private policy"} {
				require.NotContains(t, rec.Body.String(), hidden)
			}
			if !strings.Contains(path, "/models/") {
				require.Contains(t, rec.Body.String(), `"code":"context_length_exceeded"`)
			}
		})
	}
}

func TestAPIKeyContextTokenLimitChangeInvalidatesQueuedIdentity(t *testing.T) {
	key := &service.APIKey{ID: 1, AllowLongContext: true, ContextTokenLimit: 1234}
	identity := captureAPIKeyQueueIdentity(key)
	require.False(t, apiKeyQueueCoreIdentityChanged(identity, key))
	for _, limit := range []int{0, 100, 2000} {
		key.ContextTokenLimit = limit
		require.True(t, apiKeyQueueCoreIdentityChanged(identity, key))
	}
}
