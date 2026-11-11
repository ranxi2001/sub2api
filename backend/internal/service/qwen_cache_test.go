package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestQwenSessionCacheUsesNativeResponsesAndPreservesContinuation(t *testing.T) {
	account := &Account{ID: 17, Platform: PlatformQwen, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "fixture", "api_protocol": "adaptive",
			"base_url":                 "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1",
			qwenSessionCacheCredential: true}}
	response := `{"id":"resp_qwen_next","object":"response","status":"completed","model":"qwen3.8-max","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":4000,"output_tokens":2,"input_tokens_details":{"cached_tokens":3000,"cache_creation_input_tokens":900}}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}}
	svc := &OpenAIGatewayService{cfg: deepSeekChatFallbackTestConfig(), httpUpstream: upstream}
	body := `{"model":"qwen3.8-max","input":"continue","previous_response_id":"resp_qwen_first","store":true,"stream":false,"max_output_tokens":128}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	result, err := svc.Forward(context.Background(), c, account, []byte(body))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "enable", upstream.lastReq.Header.Get("x-dashscope-session-cache"))
	require.Equal(t, "resp_qwen_first", gjson.GetBytes(upstream.lastBody, "previous_response_id").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.Equal(t, int64(128), gjson.GetBytes(upstream.lastBody, "max_output_tokens").Int())
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(response))
	require.True(t, ok)
	require.Equal(t, 3000, usage.CacheReadInputTokens)
	require.Equal(t, 900, usage.CacheCreationInputTokens)
}

func TestQwenCacheSwitchesDoNotChangeLegacyOrOtherPlatforms(t *testing.T) {
	account := &Account{Platform: PlatformQwen, Type: AccountTypeAPIKey, Credentials: map[string]any{}}
	require.Equal(t, APIProtocolChatCompletions, resolveUpstreamProtocol(account, APIProtocolResponses, "qwen3.8-max", nil))
	account.Credentials[qwenSessionCacheCredential] = true
	require.Equal(t, APIProtocolResponses, resolveUpstreamProtocol(account, APIProtocolResponses, "qwen3.8-max", nil))
	account.Platform = PlatformOpenAI
	require.False(t, account.QwenSessionCacheEnabled())
	require.False(t, account.QwenStrictSessionAffinity())
}

func TestQwenNestedCacheCreationUsage(t *testing.T) {
	for _, field := range []string{"input_tokens_details", "prompt_tokens_details"} {
		usage := gjson.Parse(`{"` + field + `":{"cache_creation_input_tokens":1234,"cached_tokens":5678}}`)
		require.Equal(t, 1234, openAICacheCreationTokensFromUsage(usage))
		require.Equal(t, 5678, openAICacheReadTokensFromUsage(usage))
	}
}
