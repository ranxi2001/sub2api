package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func openaiPlatformDeepSeekAccount() *Account {
	return &Account{
		ID:       18,
		Name:     "openai-deepseek-apikey",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
		},
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": DefaultDeepseekBaseURL,
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func deepSeekChatFallbackTestConfig() *config.Config {
	return &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:           false,
				AllowInsecureHTTP: true,
			},
		},
	}
}

func TestEnsureDeepSeekChatReasoningPlaceholders(t *testing.T) {
	deepSeekAccount := openaiPlatformDeepSeekAccount()
	nativeDeepSeek := &Account{Platform: PlatformDeepseek, Type: AccountTypeAPIKey}
	otherOpenAI := &Account{
		Status:      StatusActive,
		Schedulable: true,

		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://upstream.example"},
	}

	missing := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`)
	withPlain := []byte(`{"model":"deepseek-chat","messages":[{"role":"assistant","reasoning_content":"real thinking","content":"ok"}]}`)

	t.Run("openai_platform_deepseek_fills_empty_assistant", func(t *testing.T) {
		got := ensureDeepSeekChatReasoningPlaceholders(deepSeekAccount, missing)
		require.Equal(t, deepSeekChatReasoningPlaceholderText, gjson.GetBytes(got, "messages.1.reasoning_content").String())
		require.False(t, gjson.GetBytes(got, "messages.0.reasoning_content").Exists())
		require.False(t, gjson.GetBytes(got, "messages.2.reasoning_content").Exists())
	})

	t.Run("native_deepseek_platform_fills_empty_assistant", func(t *testing.T) {
		got := ensureDeepSeekChatReasoningPlaceholders(nativeDeepSeek, missing)
		require.Equal(t, deepSeekChatReasoningPlaceholderText, gjson.GetBytes(got, "messages.1.reasoning_content").String())
	})

	t.Run("does_not_overwrite_plaintext", func(t *testing.T) {
		got := ensureDeepSeekChatReasoningPlaceholders(deepSeekAccount, withPlain)
		require.Equal(t, "real thinking", gjson.GetBytes(got, "messages.0.reasoning_content").String())
		require.Equal(t, string(withPlain), string(got), "已有明文时必须原样返回")
	})

	t.Run("non_deepseek_upstream_unchanged", func(t *testing.T) {
		body := bytes.ReplaceAll(missing, []byte("deepseek-chat"), []byte("gpt-4.1"))
		got := ensureDeepSeekChatReasoningPlaceholders(otherOpenAI, body)
		require.Equal(t, string(body), string(got))
		require.False(t, gjson.GetBytes(got, "messages.1.reasoning_content").Exists())
	})

	t.Run("nil_account_unchanged", func(t *testing.T) {
		got := ensureDeepSeekChatReasoningPlaceholders(nil, missing)
		require.Equal(t, string(missing), string(got))
	})

	t.Run("mistral_platform_deepseek_model_unchanged", func(t *testing.T) {
		mistralAccount := &Account{Platform: PlatformMistral, Type: AccountTypeAPIKey}
		body := bytes.ReplaceAll(missing, []byte("deepseek-chat"), []byte("deepseek-v4-1-flash-1017"))
		got := ensureDeepSeekChatReasoningPlaceholders(mistralAccount, body)
		require.Equal(t, string(body), string(got))
		require.False(t, gjson.GetBytes(got, "messages.1.reasoning_content").Exists())
	})
}

func TestForwardResponses_DeepSeekReasoningUsesOutboundModel(t *testing.T) {
	for _, tc := range []struct {
		name, requested, upstream string
		mapping                   map[string]any
		wantPlaceholder           bool
	}{
		{"mapped_deepseek", "gpt-5.6-sol", "deepseek-chat", map[string]any{"gpt-5.6-sol": "deepseek-chat"}, true},
		{"mixed_non_deepseek", "gpt-5.6-sol", "gpt-4.1", map[string]any{"gpt-5.6-sol": "gpt-4.1", "deepseek-alias": "deepseek-chat"}, false},
		{"direct_deepseek", "deepseek-chat", "deepseek-chat", nil, true},
		{"deepseek_alias_to_other_model", "deepseek-alias", "gpt-4.1", map[string]any{"deepseek-alias": "gpt-4.1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := openaiPlatformDeepSeekAccount()
			account.Credentials["base_url"] = "https://aggregator.example"
			if tc.mapping != nil {
				account.Credentials["model_mapping"] = tc.mapping
			}
			body := bytes.ReplaceAll(deepSeekChatHistoryWithEncryptedReasoning(), []byte("gpt-5.6-sol"), []byte(tc.requested))
			c := newDeepSeekChatFallbackContext(t, body)
			upstream := newOKChatCompletionsUpstream("rid_model_scope", deepSeekChatFallbackOKBody)
			svc := &OpenAIGatewayService{cfg: deepSeekChatFallbackTestConfig(), httpUpstream: upstream}

			result, err := svc.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tc.upstream, gjson.GetBytes(upstream.lastBody, "model").String())
			reasoning := gjson.GetBytes(upstream.lastBody, "messages.0.reasoning_content")
			if tc.wantPlaceholder {
				require.Equal(t, deepSeekChatReasoningPlaceholderText, reasoning.String())
			} else {
				require.False(t, reasoning.Exists(), "non-DeepSeek outbound models must not receive placeholders")
			}
			require.Equal(t, "call_1", gjson.GetBytes(upstream.lastBody, "messages.0.tool_calls.0.id").String())
		})
	}
}

// OpenCode Zen / Go 把 deepseek-* 模型转发给 DeepSeek，同一条 thinking-mode 400
// 会原样回吐，占位注入必须覆盖这条链路；同账号下的非 DeepSeek 模型不受影响。
func TestEnsureDeepSeekChatReasoningPlaceholders_OpenCodeZenUpstream(t *testing.T) {
	openCodeGo := &Account{
		ID:          24,
		Name:        "opencode-go",
		Platform:    PlatformOpenCodeGo,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": DefaultOpenCodeGoBaseURL},
	}
	openAIViaZen := &Account{
		ID:          25,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": DefaultOpenCodeZenBaseURL},
	}
	thirdPartyDeepSeekName := &Account{
		ID:          26,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://upstream.example"},
	}

	missingFor := func(model string) []byte {
		return []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`)
	}

	t.Run("opencode_go_deepseek_model_fills_empty_assistant", func(t *testing.T) {
		body := missingFor("deepseek-v4.1-flash")
		got := ensureDeepSeekChatReasoningPlaceholders(openCodeGo, body)
		require.Equal(t, deepSeekChatReasoningPlaceholderText, gjson.GetBytes(got, "messages.1.reasoning_content").String())
		require.False(t, gjson.GetBytes(got, "messages.0.reasoning_content").Exists())
		require.False(t, gjson.GetBytes(got, "messages.2.reasoning_content").Exists())
	})

	t.Run("opencode_go_non_deepseek_model_unchanged", func(t *testing.T) {
		body := missingFor("glm-5.3")
		got := ensureDeepSeekChatReasoningPlaceholders(openCodeGo, body)
		require.Equal(t, string(body), string(got))
	})

	t.Run("opencode_prefixed_model_id_fills_empty_assistant", func(t *testing.T) {
		body := missingFor("opencode-go/DeepSeek-V4-Pro")
		got := ensureDeepSeekChatReasoningPlaceholders(openCodeGo, body)
		require.Equal(t, deepSeekChatReasoningPlaceholderText, gjson.GetBytes(got, "messages.1.reasoning_content").String())
	})

	t.Run("openai_platform_pointed_at_zen_host_fills_empty_assistant", func(t *testing.T) {
		body := missingFor("deepseek-v4-flash")
		got := ensureDeepSeekChatReasoningPlaceholders(openAIViaZen, body)
		require.Equal(t, deepSeekChatReasoningPlaceholderText, gjson.GetBytes(got, "messages.1.reasoning_content").String())
	})

	t.Run("third_party_deepseek_model_preserves_fork_compatibility", func(t *testing.T) {
		body := missingFor("deepseek-v4.1-flash")
		got := ensureDeepSeekChatReasoningPlaceholders(thirdPartyDeepSeekName, body)
		require.Equal(t, deepSeekChatReasoningPlaceholderText, gjson.GetBytes(got, "messages.1.reasoning_content").String())
	})
}

type reasoningHitCache struct {
	stubGatewayCache
	values map[string]string
}

func (c *reasoningHitCache) GetReasoningContent(_ context.Context, itemID string) (string, error) {
	if v, ok := c.values[itemID]; ok {
		return v, nil
	}
	return "", ErrReasoningContentNotFound
}

func deepSeekChatHistoryWithEncryptedReasoning() []byte {
	return []byte(`{
		"model":"gpt-5.6-sol",
		"stream":false,
		"input":[
			{"type":"reasoning","id":"item_enc1","summary":[],"encrypted_content":"opaque"},
			{"type":"function_call","call_id":"call_1","name":"exec","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}
		]
	}`)
}

func newDeepSeekChatFallbackContext(t *testing.T, body []byte) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func newOKChatCompletionsUpstream(requestID, body string) *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{requestID}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
}

const deepSeekChatFallbackOKBody = `{"id":"chatcmpl_ph","object":"chat.completion","model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

func TestForwardResponses_DeepSeekChatFallbackInjectsReasoningPlaceholderOnCacheMiss(t *testing.T) {
	body := deepSeekChatHistoryWithEncryptedReasoning()
	c := newDeepSeekChatFallbackContext(t, body)
	upstream := newOKChatCompletionsUpstream("rid_ds_rc_placeholder", deepSeekChatFallbackOKBody)
	svc := &OpenAIGatewayService{
		cfg:          deepSeekChatFallbackTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.Forward(context.Background(), c, openaiPlatformDeepSeekAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, upstream.lastReq.URL.String(), "/chat/completions")
	require.Equal(t, deepSeekChatReasoningPlaceholderText, gjson.GetBytes(upstream.lastBody, "messages.0.reasoning_content").String())
	require.Equal(t, "call_1", gjson.GetBytes(upstream.lastBody, "messages.0.tool_calls.0.id").String())
}

func TestForwardResponses_DeepSeekChatFallbackKeepsCachedReasoningContent(t *testing.T) {
	body := deepSeekChatHistoryWithEncryptedReasoning()
	c := newDeepSeekChatFallbackContext(t, body)
	upstream := newOKChatCompletionsUpstream("rid_ds_rc_cached", deepSeekChatFallbackOKBody)
	svc := &OpenAIGatewayService{
		cfg:          deepSeekChatFallbackTestConfig(),
		httpUpstream: upstream,
		cache:        &reasoningHitCache{values: map[string]string{"item_enc1": "cached thinking"}},
	}

	result, err := svc.Forward(context.Background(), c, openaiPlatformDeepSeekAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "cached thinking", gjson.GetBytes(upstream.lastBody, "messages.0.reasoning_content").String())
}

func TestForwardResponses_NonDeepSeekChatFallbackDoesNotInjectReasoningPlaceholder(t *testing.T) {
	body := deepSeekChatHistoryWithEncryptedReasoning()
	c := newDeepSeekChatFallbackContext(t, body)
	upstream := newOKChatCompletionsUpstream("rid_other_rc", deepSeekChatFallbackOKBody)
	account := &Account{
		Status:      StatusActive,
		Schedulable: true,

		ID:       99,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
		},
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "http://upstream.example",
		},
	}
	svc := &OpenAIGatewayService{
		cfg:          deepSeekChatFallbackTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, gjson.GetBytes(upstream.lastBody, "messages.0.reasoning_content").Exists())
}
