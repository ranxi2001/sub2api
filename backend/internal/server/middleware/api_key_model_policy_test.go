package middleware

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAPIKeyModelPolicyHTTPIsolationAndAdmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		mapping map[string]string
		allowed []string
		denied  []string
		status  int
		model   string
	}{
		{"mapped responses", "/v1/responses", map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, nil, nil, 200, "gpt-5.6-sol"},
		{"mapped compact", "/v1/responses/compact", map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, nil, nil, 200, "gpt-5.6-sol"},
		{"mapped chat", "/v1/chat/completions", map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, nil, nil, 200, "gpt-5.6-sol"},
		{"other key", "/v1/responses", nil, nil, nil, 200, "gpt-6.1-sol"},
		{"unmatched model", "/v1/responses", map[string]string{"other": "gpt-5.6-sol"}, nil, nil, 200, "gpt-6.1-sol"},
		{"identity mapping", "/v1/responses", map[string]string{"gpt-6.1-sol": "gpt-6.1-sol"}, nil, nil, 200, "gpt-6.1-sol"},
		{"other endpoint", "/v1/images/generations", map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, nil, nil, 200, "gpt-6.1-sol"},
		{"alias denied", "/v1/responses", map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, []string{"gpt-5.6-sol"}, nil, 404, ""},
		{"target denied", "/v1/responses", map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, []string{"gpt-6.1-sol"}, nil, 404, ""},
		{"user target denied", "/v1/responses", map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, nil, []string{"gpt-5.6-sol"}, 404, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := allowlistAPIKey(len(tc.allowed) > 0, tc.allowed...)
			key.ModelMapping = tc.mapping
			key.User = &service.User{UserGroupDeniedModels: tc.denied}
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set(string(ContextKeyAPIKey), key); c.Next() })
			router.Use(GroupModelAllowlist(), APIKeyModelPolicy())
			called := false
			router.POST(tc.path, func(c *gin.Context) {
				called = true
				body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
				require.NoError(t, err)
				require.Equal(t, tc.model, gjson.GetBytes(body, "model").String())
				if tc.model == "gpt-5.6-sol" {
					require.Equal(t, tc.model, service.APIKeyMappedModelFromContext(c.Request.Context()))
					original, ok := service.RequestedPublicModelFromContext(c.Request.Context())
					require.True(t, ok)
					require.Equal(t, "gpt-6.1-sol", original)
					require.ElementsMatch(t, []string{"gpt-6.1-sol", "gpt-5.6-sol"}, service.APIKeyQueueRequestPermissionsFromContext(c.Request.Context()).Models)
				} else {
					require.Empty(t, service.APIKeyMappedModelFromContext(c.Request.Context()), "only actual model rewrites affect billing")
				}
				c.Status(http.StatusOK)
			})
			w := doJSON(t, router, http.MethodPost, tc.path, `{"model":"gpt-6.1-sol","input":"hello"}`)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Equal(t, tc.status == 200, called)
		})
	}
}

func TestAPIKeyModelPolicyChangeRequiresFreshAdmission(t *testing.T) {
	key := &service.APIKey{ID: 1, ModelMapping: map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, ReasoningEffort: "high"}
	identity := captureAPIKeyQueueIdentity(key)
	require.False(t, apiKeyQueueCoreIdentityChanged(identity, key))
	key.ModelMapping["gpt-6.1-sol"] = "gpt-5.6-luna"
	require.True(t, apiKeyQueueCoreIdentityChanged(identity, key), "capture must own a copy of the old policy")
	key.ModelMapping["gpt-6.1-sol"] = "gpt-5.6-sol"
	key.ReasoningEffort = "low"
	require.True(t, apiKeyQueueCoreIdentityChanged(identity, key))
	key.ReasoningEffort = "high"
	require.False(t, apiKeyQueueCoreIdentityChanged(identity, key))
}

func TestAPIKeyModelPolicyCanonicalizesConflictingModelFields(t *testing.T) {
	key := &service.APIKey{ModelMapping: map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(string(ContextKeyAPIKey), key); c.Next() })
	router.Use(APIKeyModelPolicy())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
		require.NoError(t, err)
		var typed struct {
			Model string `json:"model"`
		}
		require.NoError(t, json.Unmarshal(body, &typed))
		require.Equal(t, "unmapped", typed.Model)
		require.Equal(t, typed.Model, gjson.GetBytes(body, "model").String())
		require.Empty(t, service.APIKeyMappedModelFromContext(c.Request.Context()), "canonicalization alone is not a key model rewrite")
		c.Status(http.StatusOK)
	})
	w := doJSON(t, router, http.MethodPost, "/v1/chat/completions", `{"model":"unmapped","Model":"gpt-6.1-sol","messages":[]}`)
	require.Equal(t, http.StatusOK, w.Code)
}
