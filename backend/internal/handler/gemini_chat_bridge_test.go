package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestServeGeminiViaChatCompletionsUsesChatEndpointShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-4o:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	c.Params = gin.Params{{Key: "modelAction", Value: "/gpt-4o:generateContent"}}

	var seenPath string
	var seenBody string
	ServeGeminiViaChatCompletions(c, func(c *gin.Context) {
		seenPath = c.Request.URL.Path
		body, readErr := io.ReadAll(c.Request.Body)
		require.NoError(t, readErr)
		seenBody = string(body)
		c.Header("Content-Type", "application/json")
		c.JSON(http.StatusOK, gin.H{
			"model": "gpt-4o",
			"choices": []gin.H{{
				"index":         0,
				"message":       gin.H{"role": "assistant", "content": "hi"},
				"finish_reason": "stop",
			}},
			"usage": gin.H{"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3},
		})
	})

	require.Equal(t, "/v1beta/models/gpt-4o:generateContent", seenPath)
	require.Equal(t, "gpt-4o", gjson.Get(seenBody, "model").String())
	require.Equal(t, "hello", gjson.Get(seenBody, "messages.0.content").String())
	require.False(t, gjson.Get(seenBody, "contents").Exists())
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "hi", gjson.Get(recorder.Body.String(), "candidates.0.content.parts.0.text").String())
	require.Equal(t, "STOP", gjson.Get(recorder.Body.String(), "candidates.0.finishReason").String())
	require.False(t, gjson.Get(recorder.Body.String(), "choices").Exists())
}

func TestServeGeminiViaChatCompletionsUsesResolvedUpstreamModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/public-model:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), service.CompositeRouteDecision{
		Matched:        true,
		PublicModel:    "public-model",
		TargetPlatform: service.PlatformOpenAI,
		UpstreamModel:  "gpt-4o-2024-08-06",
	}))
	c.Params = gin.Params{{Key: "modelAction", Value: "/public-model:generateContent"}}

	var seenModel string
	ServeGeminiViaChatCompletions(c, func(c *gin.Context) {
		body, readErr := io.ReadAll(c.Request.Body)
		require.NoError(t, readErr)
		seenModel = gjson.GetBytes(body, "model").String()
		c.JSON(http.StatusOK, gin.H{
			"model": seenModel,
			"choices": []gin.H{{
				"index":         0,
				"message":       gin.H{"role": "assistant", "content": "hi"},
				"finish_reason": "stop",
			}},
		})
	})

	require.Equal(t, "gpt-4o-2024-08-06", seenModel)
	require.Equal(t, "gpt-4o-2024-08-06", gjson.Get(recorder.Body.String(), "modelVersion").String())
}

func TestServeGeminiViaChatCompletionsStreamsGeminiSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-4o:streamGenerateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	c.Params = gin.Params{{Key: "modelAction", Value: "/gpt-4o:streamGenerateContent"}}

	ServeGeminiViaChatCompletions(c, func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n"))
	})

	require.Contains(t, recorder.Body.String(), `"text":"hi"`)
	require.Contains(t, recorder.Body.String(), `"finishReason":"STOP"`)
	require.NotContains(t, recorder.Body.String(), "[DONE]")
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
}

func TestServeGeminiViaChatCompletionsForwardsSSEKeepalive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-4o:streamGenerateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	c.Params = gin.Params{{Key: "modelAction", Value: "/gpt-4o:streamGenerateContent"}}

	ServeGeminiViaChatCompletions(c, func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write([]byte(": keepalive\n\n"))
		_, _ = c.Writer.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"))
	})

	require.Contains(t, recorder.Body.String(), ": keepalive\n\n")
	require.Contains(t, recorder.Body.String(), `"text":"hi"`)
}

func TestServeGeminiViaChatCompletionsConvertsUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-4o:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	c.Params = gin.Params{{Key: "modelAction", Value: "/gpt-4o:generateContent"}}

	ServeGeminiViaChatCompletions(c, func(c *gin.Context) {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"message": "billing denied", "type": "permission_error"}})
	})

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Equal(t, "billing denied", gjson.Get(recorder.Body.String(), "error.message").String())
	require.Equal(t, "PERMISSION_DENIED", gjson.Get(recorder.Body.String(), "error.status").String())
	require.False(t, gjson.Get(recorder.Body.String(), "choices").Exists())
}

func TestServeGeminiViaChatCompletionsCountTokensDoesNotCallUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-4o:countTokens", strings.NewReader(`{"contents":[{"parts":[{"text":"abcd"}]}]}`))
	c.Params = gin.Params{{Key: "modelAction", Value: "/gpt-4o:countTokens"}}

	called := false
	ServeGeminiViaChatCompletions(c, func(c *gin.Context) { called = true })
	require.False(t, called)
	require.Equal(t, int64(1), gjson.Get(recorder.Body.String(), "totalTokens").Int())
}

func TestGeminiInboundUsesNativeUpstreamOnlyForGeminiFamily(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func(platform string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-4o:generateContent", nil)
		groupID := int64(1)
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: platform}})
		return c
	}
	require.True(t, GeminiInboundUsesNativeUpstream(newCtx(service.PlatformGemini)))
	require.True(t, GeminiInboundUsesNativeUpstream(newCtx(service.PlatformAntigravity)))
	require.False(t, GeminiInboundUsesNativeUpstream(newCtx(service.PlatformOpenAI)))
	require.False(t, GeminiInboundUsesNativeUpstream(newCtx(service.PlatformAnthropic)))
	require.False(t, GeminiInboundUsesNativeUpstream(newCtx(service.PlatformDeepseek)))
}

func TestBridgedGeminiModelListDoesNotRequireGeminiAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	groupID := int64(7)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
	})
	(&GatewayHandler{}).GeminiV1BetaListModels(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotEmpty(t, gjson.Get(recorder.Body.String(), "models").Array())
	require.Contains(t, gjson.Get(recorder.Body.String(), "models.0.name").String(), "models/")
}
