package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestQwenEffortUsesMappedModelAndKeepsRequestedValue(t *testing.T) {
	for _, tc := range []struct{ model, request, wire string }{
		{"qwen3.8-max", "max", "xhigh"},
		{"qwen3.8-max-0902", "high", "xhigh"},
		{"qwen3.8-flash", "minimal", "low"},
		{"qwen3.8-27b", "medium", "medium"},
		{"qwen3.8-omni-flash", "none", "none"},
		{"qwen4-max", "max", "xhigh"},
		{"qwen4-max", "medium", "high"},
		{"qwen4-max", "minimal", "low"},
		{"qwen4-max-preview", "medium", "medium"},
		{"qwen3.7-plus", "high", "high"},
	} {
		t.Run(tc.model+"/"+tc.request, func(t *testing.T) {
			account := &Account{Platform: PlatformQwen, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"model_mapping": map[string]any{"client-model": tc.model},
			}}
			for path, template := range map[string]string{
				"reasoning.effort":     `{"model":"client-model","reasoning":{"effort":"%s"}}`,
				"reasoning_effort":     `{"model":"client-model","reasoning_effort":"%s"}`,
				"output_config.effort": `{"model":"client-model","output_config":{"effort":"%s"}}`,
			} {
				in := []byte(fmt.Sprintf(template, tc.request))
				out := normalizeQwenRequestEffort(account, in, "", path)
				require.Equal(t, tc.request, gjson.GetBytes(in, path).Str)
				require.Equal(t, tc.wire, gjson.GetBytes(out, path).Str)
			}
		})
	}
}

func TestQwenExplicitNoneSurvivesResponsesAndWSBridge(t *testing.T) {
	account := &Account{Platform: PlatformQwen, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"qwen3.8-max","reasoning":{"effort":"none","summary":"auto"}}`)
	filtered, err := filterOpenAIResponsesNoneReasoningEffortForAccount(account, body)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(filtered))
	bridged, err := prepareOpenAIWSHTTPBridgeSessionBody(account, body, false)
	require.NoError(t, err)
	require.Equal(t, "none", gjson.GetBytes(bridged, "reasoning.effort").Str)
}

func TestQwenBridgedMessagesWireAndUsage(t *testing.T) {
	account := &Account{Platform: PlatformQwen, Type: AccountTypeAPIKey}
	service := &OpenAIGatewayService{}
	body := []byte(`{"model":"qwen3.8-max","output_config":{"effort":"max","format":{"type":"json_schema"}},"thinking":{"type":"enabled","budget_tokens":32768}}`)
	for _, effort := range []string{"high", "none"} {
		source := []byte(fmt.Sprintf(`{"reasoning_effort":"%s"}`, effort))
		_, wire, err := service.buildNativeAnthropicUpstreamRequest(context.Background(), nil, account, body, "mock", "https://example.invalid/v1/messages", source)
		require.NoError(t, err)
		want := "xhigh"
		if effort == "none" {
			want = "none"
			require.Equal(t, "disabled", gjson.GetBytes(wire, "thinking.type").Str)
			require.False(t, gjson.GetBytes(wire, "output_config.effort").Exists())
		} else {
			require.Equal(t, want, gjson.GetBytes(wire, "output_config.effort").Str)
		}
		require.False(t, gjson.GetBytes(wire, "thinking.budget_tokens").Exists())
		require.Equal(t, "json_schema", gjson.GetBytes(wire, "output_config.format.type").Str)
		require.Equal(t, want, *nativeMessagesUsageEffort(account, wire, "qwen3.8-max"))
	}
}
