package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRequiresDeepSeekChatReasoning_SkipsMistralPlatform(t *testing.T) {
	mistral := &Account{Platform: PlatformMistral, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"assistant","content":"ok"}]}`)
	require.False(t, requiresDeepSeekChatReasoning(mistral, body))
	require.Equal(t, string(body), string(ensureDeepSeekChatReasoningPlaceholders(mistral, body)))
}

func TestStripDisallowedMistralChatMessageExtras(t *testing.T) {
	mistral := &Account{Platform: PlatformMistral}
	deepseek := &Account{Platform: PlatformDeepseek}
	body := []byte(`{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"assistant","content":"ok","reasoning_content":" ","reasoning_details":[{"type":"text"}]},{"role":"user","content":"hi"}]}`)

	got := stripDisallowedMistralChatMessageExtras(mistral, body)
	require.False(t, gjson.GetBytes(got, "messages.0.reasoning_content").Exists())
	require.False(t, gjson.GetBytes(got, "messages.0.reasoning_details").Exists())
	require.Equal(t, "ok", gjson.GetBytes(got, "messages.0.content").String())
	require.Equal(t, "hi", gjson.GetBytes(got, "messages.1.content").String())

	unchanged := stripDisallowedMistralChatMessageExtras(deepseek, body)
	require.Equal(t, string(body), string(unchanged))

	// Edge cases: nil account, empty body, non-array messages, already clean messages
	require.Equal(t, string(body), string(stripDisallowedMistralChatMessageExtras(nil, body)))
	require.Empty(t, stripDisallowedMistralChatMessageExtras(mistral, nil))
	nonArrayMsgs := []byte(`{"model":"deepseek-v4-1-flash-1017","messages":"invalid"}`)
	require.Equal(t, string(nonArrayMsgs), string(stripDisallowedMistralChatMessageExtras(mistral, nonArrayMsgs)))
	cleanBody := []byte(`{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"assistant","content":"ok"},{"role":"user","content":"hi"}]}`)
	require.Equal(t, string(cleanBody), string(stripDisallowedMistralChatMessageExtras(mistral, cleanBody)))
}

func TestStripMistralExtrasPreservesMessageAndRootExtensions(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	body := []byte(`{"model":"deepseek-v4-1-flash-1017","metadata":{"reasoning_content":"root extension"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"kept"}]}],"reasoning_content":null,"reasoning_details":[],"tool_calls":[{"id":"call_test","type":"function","function":{"name":"sum","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_test","content":"7","reasoning_details":[]},{"role":"user","content":"continue"}]}`)
	got := stripDisallowedMistralChatMessageExtras(account, body)
	require.True(t, json.Valid(got))
	for _, message := range gjson.GetBytes(got, "messages").Array() {
		require.False(t, message.Get("reasoning_content").Exists())
		require.False(t, message.Get("reasoning_details").Exists())
	}
	require.Equal(t, "root extension", gjson.GetBytes(got, "metadata.reasoning_content").String())
	require.Equal(t, "kept", gjson.GetBytes(got, "messages.0.content.0.thinking.0.text").String())
	require.Equal(t, "call_test", gjson.GetBytes(got, "messages.0.tool_calls.0.id").String())
	require.Equal(t, "call_test", gjson.GetBytes(got, "messages.1.tool_call_id").String())
	require.Equal(t, "7", gjson.GetBytes(got, "messages.1.content").String())
	require.Equal(t, "continue", gjson.GetBytes(got, "messages.2.content").String())
	require.Equal(t, got, stripDisallowedMistralChatMessageExtras(account, got))
}

func BenchmarkStripDisallowedMistralChatMessageExtras(b *testing.B) {
	for _, count := range []int{1000, 4000} {
		for _, extras := range []bool{false, true} {
			b.Run(fmt.Sprintf("messages=%d/extras=%t", count, extras), func(b *testing.B) {
				message := json.RawMessage(`{"role":"assistant","content":"ok"}`)
				if extras {
					message = json.RawMessage(`{"role":"assistant","content":"ok","reasoning_content":" ","reasoning_details":[]}`)
				}
				messages := make([]json.RawMessage, count)
				for index := range messages {
					messages[index] = message
				}
				body, err := json.Marshal(map[string]any{"model": "deepseek-test", "messages": messages})
				if err != nil {
					b.Fatal(err)
				}
				account := &Account{Platform: PlatformMistral}
				b.ReportAllocs()
				b.ResetTimer()
				for index := 0; index < b.N; index++ {
					_ = stripDisallowedMistralChatMessageExtras(account, body)
				}
			})
		}
	}
}

func TestNormalizeThenEnsureDoesNotReinjectOnMistral(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	in := []byte(`{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello","reasoning_content":"think"}]}`)
	normalized, err := normalizeMistralChatRequest(account, in)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(normalized, "messages.1.reasoning_content").Exists())

	ooutbound := ensureDeepSeekChatReasoningPlaceholders(account, normalized)
	ooutbound = stripDisallowedMistralChatMessageExtras(account, ooutbound)
	require.False(t, gjson.GetBytes(ooutbound, "messages.1.reasoning_content").Exists())
	require.NotEqual(t, "", gjson.GetBytes(ooutbound, "messages.1.content").Raw)
}
