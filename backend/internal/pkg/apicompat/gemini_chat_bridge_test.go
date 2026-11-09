package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiGenerateContentToChatCompletions(t *testing.T) {
	body := []byte(`{
		"systemInstruction": {"parts": [{"text": "be brief"}]},
		"contents": [
			{"role": "user", "parts": [{"text": "hi"}]},
			{"role": "model", "parts": [
				{"text": "calling"},
				{"functionCall": {"name": "lookup", "args": {"q": "weather"}}}
			]},
			{"role": "user", "parts": [
				{"functionResponse": {"name": "lookup", "response": {"content": "sunny"}}},
				{"text": "thanks"}
			]}
		],
		"tools": [{"functionDeclarations": [{"name": "lookup", "description": "look", "parameters": {"type": "object"}}]}],
		"toolConfig": {"functionCallingConfig": {"mode": "ANY"}},
		"generationConfig": {"temperature": 0.2, "maxOutputTokens": 32, "responseMimeType": "application/json"}
	}`)

	got, err := GeminiGenerateContentToChatCompletions("gpt-4o-mini", body, false)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-mini", gjson.GetBytes(got, "model").String())
	require.False(t, gjson.GetBytes(got, "stream").Bool())
	require.Equal(t, "system", gjson.GetBytes(got, "messages.0.role").String())
	require.Equal(t, "be brief", gjson.GetBytes(got, "messages.0.content").String())
	require.Equal(t, "user", gjson.GetBytes(got, "messages.1.role").String())
	require.Equal(t, "assistant", gjson.GetBytes(got, "messages.2.role").String())
	require.Equal(t, "lookup", gjson.GetBytes(got, "messages.2.tool_calls.0.function.name").String())
	require.Equal(t, "tool", gjson.GetBytes(got, "messages.3.role").String())
	require.Equal(t, gjson.GetBytes(got, "messages.2.tool_calls.0.id").String(), gjson.GetBytes(got, "messages.3.tool_call_id").String())
	require.Equal(t, "sunny", gjson.GetBytes(got, "messages.3.content").String())
	require.Equal(t, "thanks", gjson.GetBytes(got, "messages.4.content").String())
	require.Equal(t, "lookup", gjson.GetBytes(got, "tools.0.function.name").String())
	require.Equal(t, "required", gjson.GetBytes(got, "tool_choice").String())
	require.Equal(t, int64(32), gjson.GetBytes(got, "max_tokens").Int())
	require.Equal(t, "json_object", gjson.GetBytes(got, "response_format.type").String())
	require.False(t, gjson.GetBytes(got, "contents").Exists())
}

func TestGeminiGenerateContentToChatCompletionsKeepsVendorModel(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`)
	got, err := GeminiGenerateContentToChatCompletions("deepseek/deepseek-v4-flash", body, true)
	require.NoError(t, err)
	require.Equal(t, "deepseek/deepseek-v4-flash", gjson.GetBytes(got, "model").String())
	require.True(t, gjson.GetBytes(got, "stream").Bool())
	require.True(t, gjson.GetBytes(got, "stream_options.include_usage").Bool())
}

func TestGeminiGenerateContentToChatCompletionsRejectsCachedContent(t *testing.T) {
	body := []byte(`{"cachedContent":"cachedContents/abc","contents":[{"role":"user","parts":[{"text":"x"}]}]}`)
	_, err := GeminiGenerateContentToChatCompletions("gpt-4o", body, false)
	require.Error(t, err)
}

func TestChatCompletionsJSONToGemini(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-1",
		"model":"gpt-4o-mini",
		"choices":[{
			"index":0,
			"message":{"role":"assistant","content":"hello","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"weather\"}"}}]},
			"finish_reason":"tool_calls"
		}],
		"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}
	}`)
	got, err := ChatCompletionsJSONToGemini(body, "fallback")
	require.NoError(t, err)
	require.Equal(t, "hello", gjson.GetBytes(got, "candidates.0.content.parts.0.text").String())
	require.Equal(t, "lookup", gjson.GetBytes(got, "candidates.0.content.parts.1.functionCall.name").String())
	require.Equal(t, "weather", gjson.GetBytes(got, "candidates.0.content.parts.1.functionCall.args.q").String())
	require.Equal(t, "model", gjson.GetBytes(got, "candidates.0.content.role").String())
	require.Equal(t, "STOP", gjson.GetBytes(got, "candidates.0.finishReason").String())
	require.Equal(t, int64(3), gjson.GetBytes(got, "usageMetadata.promptTokenCount").Int())
	require.Equal(t, "gpt-4o-mini", gjson.GetBytes(got, "modelVersion").String())
}

func TestGeminiSSETranslator(t *testing.T) {
	translator := NewGeminiSSETranslator("gpt-4o")
	var out []byte
	out = append(out, translator.Push([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hel\"}}]}\n"))...)
	out = append(out, translator.Push([]byte("\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3},\"model\":\"gpt-4o\"}\n\ndata: [DONE]\n"))...)
	require.Contains(t, string(out), `"text":"hel"`)
	require.Contains(t, string(out), `"text":"lo"`)
	require.Contains(t, string(out), `"finishReason":"STOP"`)
	require.Contains(t, string(out), `"promptTokenCount":1`)
	require.NotContains(t, string(out), "[DONE]")
	require.True(t, json.Valid([]byte(gjson.Get(firstSSEPayload(string(out)), "@this").Raw)))
}

func TestEstimateGeminiTotalTokens(t *testing.T) {
	tokens, characters := EstimateGeminiTotalTokens([]byte(`{"systemInstruction":{"parts":[{"text":"abcd"}]},"contents":[{"parts":[{"text":"efgh"}]}]}`))
	require.Equal(t, 9, characters)
	require.Equal(t, 3, tokens)
}

func firstSSEPayload(body string) string {
	const prefix = "data: "
	start := len(prefix)
	if len(body) < start {
		return ""
	}
	rest := body[start:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '\n' {
			return rest[:i]
		}
	}
	return rest
}
