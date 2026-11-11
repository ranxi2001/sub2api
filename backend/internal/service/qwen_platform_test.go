package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestQwenPlatformKeepsIdentityAndAPIKeyContract(t *testing.T) {
	require.True(t, domain.UsesOpenAIGateway(PlatformQwen))
	require.Equal(t, PlatformQwen, NormalizeOpenAICompatiblePlatform(PlatformQwen))
	require.False(t, IsCNProvider(PlatformQwen), "no balance or Coding Plan probe is implemented")
	profile := LookupProviderProfile(PlatformQwen)
	require.NotNil(t, profile)
	require.False(t, profile.SupportsProtocol(AccountModePayG, APIProtocolResponses))
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeOAuth, AccountTypeSetupToken} {
		account, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformQwen, Type: kind, Credentials: map[string]any{"api_key": "fixture-key"}}, map[string]any{})
		if kind != AccountTypeAPIKey {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, PlatformQwen, account.Platform)
		require.Equal(t, DefaultQwenBaseURL, account.GetOpenAIBaseURL())
		require.False(t, account.IsOpenAIApiKey())
		for _, inbound := range []string{APIProtocolChatCompletions, APIProtocolResponses, APIProtocolAnthropic} {
			require.Equal(t, APIProtocolChatCompletions, resolveUpstreamProtocol(account, inbound, "qwen3.8-max", nil))
		}
	}
	for _, model := range []string{"qwen3.8-max", "qwen-plus", "qwen2.5-coder-32b", "dashscope/qwen-plus", "qwen/qwen-plus"} {
		platform, ok := DetectModelPlatform(model)
		require.True(t, ok, model)
		require.Equal(t, PlatformQwen, platform)
	}
	_, ok := DetectModelPlatform("qwenish-unrelated")
	require.False(t, ok)
}

func TestQwenCompatibleForwarding(t *testing.T) {
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(endpoint+map[bool]string{false: "/buffered", true: "/stream"}[stream], func(t *testing.T) {
				account := &Account{ID: 91, Platform: PlatformQwen, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://relay.example.com/compatible-mode/v1", "model_mapping": map[string]any{"public-qwen": "qwen3.8-max"}}}
				response := `{"id":"chatcmpl-qwen","object":"chat.completion","model":"qwen3.8-max","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
				contentType := "application/json"
				if stream {
					contentType = "text/event-stream"
					response = "data: " + `{"id":"chatcmpl-qwen","object":"chat.completion.chunk","model":"qwen3.8-max","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}` + "\n\ndata: " + `{"id":"chatcmpl-qwen","object":"chat.completion.chunk","model":"qwen3.8-max","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}` + "\n\ndata: [DONE]\n\n"
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response))}}
				svc := &OpenAIGatewayService{cfg: deepSeekChatFallbackTestConfig(), httpUpstream: upstream}
				body := `{"model":"public-qwen","messages":[{"role":"user","content":"hi"}],"stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
				if endpoint == "/v1/responses" {
					body = `{"model":"public-qwen","input":"hi","stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(body))
				c.Request.Header.Set("Content-Type", "application/json")
				var result *OpenAIForwardResult
				var err error
				if endpoint == "/v1/responses" {
					result, err = svc.Forward(context.Background(), c, account, []byte(body))
				} else {
					result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), "", "")
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, "https://relay.example.com/compatible-mode/v1/chat/completions", upstream.lastReq.URL.String())
				require.Equal(t, "Bearer fixture-key", upstream.lastReq.Header.Get("Authorization"))
				require.Equal(t, "qwen3.8-max", gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, PlatformQwen, account.Platform)
				require.Contains(t, recorder.Body.String(), "ok")
			})
		}
	}
}
