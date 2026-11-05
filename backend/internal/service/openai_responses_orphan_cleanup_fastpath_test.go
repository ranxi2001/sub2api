package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOrphanCleanupFastRejectionMatchesFullDecode(t *testing.T) {
	for _, body := range []string{
		`{"input":[{"type":"message","role":"user","content":"ordinary"}]}`,
		`{"input":[{"type":"message","id":"msg_1"}],"n":1e9999}`,
		`{"input":[{"type":"message"}],"input":[{"type":"reasoning"}]}`,
		`{"input":[],"type":"other"}`,
	} {
		raw := []byte(body)
		require.False(t, openAIResponsesMayNeedOrphanCleanup(raw), body)
		var decoded map[string]any
		require.NoError(t, decodeOpenAIJSONUseNumber(raw, &decoded))
		input, _ := decoded["input"].([]any)
		require.False(t, sanitizeOpenAIResponsesOrphanToolOutputs(decoded, input, false), body)
	}
}

func TestOrphanCleanupFastRejectionRetainsFallback(t *testing.T) {
	for _, body := range []string{
		`{"input":[{"type":"function_call_output","call_id":"missing"}]}`,
		`{"input":[{"type":"tool_search_output","call_id":"missing"}]}`,
		`{"input":[{"type":"custom_tool_call_output","call_id":"missing"}]}`,
		`{"input":[{"type":"mcp_tool_call_output","call_id":"missing"}]}`,
		`{"input":[{"type":"function_call_\u006futput","call_id":"missing"}]}`,
		`{"input":[{"type":"message","content":"contains _output as data"}]}`,
		`{"input":[`,
		`{"input":[]} {}`,
	} {
		require.True(t, openAIResponsesMayNeedOrphanCleanup([]byte(body)), body)
	}
	// If a future supported output type lacks the shared marker, decoding must
	// remain enabled even when that marker is absent from the request.
	types := append([]string(nil), codexToolCallOutputItemTypes[:]...)
	types = append(types, "tool_result")
	require.True(t, openAIResponsesMayNeedOrphanCleanupWithTypes([]byte(`{"input":[{"type":"tool_result"}]}`), types))
}

func TestWebSocketOrphanCleanupFastPathPreservesSemantics(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for _, outputType := range []string{"function_call_output", "tool_search_output", "custom_tool_call_output", "mcp_tool_call_output"} {
		t.Run(outputType, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"model": "gpt-5.4",
				"input": []any{
					map[string]any{"type": outputType, "call_id": "missing", "output": "result"},
					map[string]any{"type": "message", "role": "user", "content": "hello"},
				},
			})
			require.NoError(t, err)
			got, changed, err := normalizeOpenAIResponsesWebSocketCompatibilityBody(body, account, false)
			require.NoError(t, err)
			require.True(t, changed)
			require.Len(t, gjson.GetBytes(got, "input").Array(), 1)
			require.Equal(t, "message", gjson.GetBytes(got, "input.0.type").String())
		})
	}
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"function_call_\u006futput","call_id":"missing","output":"result"},{"type":"message","role":"user","content":"hello"}]}`)
	got, changed, err := normalizeOpenAIResponsesWebSocketCompatibilityBody(body, account, false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, gjson.GetBytes(got, "input").Array(), 1)
	require.Equal(t, "message", gjson.GetBytes(got, "input.0.type").String())

	// The fast path preserves an ordinary history; fallback also preserves
	// matched, named standalone and previous-response-dependent outputs.
	for _, fixture := range []string{
		`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"ordinary"}]}`,
		`{"model":"gpt-5.4","input":[{"type":"function_call","call_id":"c"},{"type":"function_call_output","call_id":"c"}]}`,
		`{"model":"gpt-5.4","input":[{"type":"function_call_output","name":"delegate","output":"standalone"}]}`,
		`{"model":"gpt-5.4","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"remote"}]}`,
	} {
		got, _, err := normalizeOpenAIResponsesWebSocketCompatibilityBody([]byte(fixture), account, false)
		require.NoError(t, err)
		require.JSONEq(t, gjson.Get(fixture, "input").Raw, gjson.GetBytes(got, "input").Raw)
	}
}

func FuzzOrphanCleanupFastRejection(f *testing.F) {
	for _, body := range []string{
		`{"input":[{"type":"message","content":"plain"}]}`,
		`{"input":[{"type":"function_call_output","call_id":"orphan"}]}`,
		`{"input":[{"type":"function_call_\u006futput","call_id":"orphan"}]}`,
		`{"input":[{"type":"message"}],"input":[{"type":"reasoning"}]}`,
	} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 || openAIResponsesMayNeedOrphanCleanup(body) || !gjson.GetBytes(body, "input").IsArray() {
			return
		}
		var decoded map[string]any
		if err := decodeOpenAIJSONUseNumber(body, &decoded); err != nil {
			t.Fatalf("fast rejection skipped a decode error: %v", err)
		}
		input, _ := decoded["input"].([]any)
		if sanitizeOpenAIResponsesOrphanToolOutputs(decoded, input, false) {
			t.Fatal("fast rejection skipped an orphan output")
		}
	})
}
