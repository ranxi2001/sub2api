package openai

import (
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"testing"
)

func TestSmartProtocolContentClassification(t *testing.T) {
	for _, tc := range []struct{ name, payload, want string }{
		{"lifecycle", `{"type":"response.created","response":{"id":"resp_synthetic"}}`, ""},
		{"empty delta", `{"type":"response.output_text.delta","delta":""}`, ""},
		{"null delta", `{"type":"response.output_text.delta","delta":null}`, ""},
		{"object delta", `{"type":"response.output_text.delta","delta":{"status":"working"}}`, ""},
		{"unknown metadata delta", `{"type":"response.metadata.delta","delta":"working"}`, ""},
		{"tool name metadata", `{"type":"response.output_item.added","item":{"type":"function_call","name":"lookup","arguments":""}}`, ""},
		{"encrypted reasoning", `{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"synthetic"}}`, ""},
		{"text", `{"type":"response.output_text.delta","delta":"ok"}`, "visible_text"},
		{"tool", `{"type":"response.function_call_arguments.delta","delta":"{}"}`, "tool_input"},
		{"reasoning", `{"type":"response.reasoning_text.delta","delta":"synthetic"}`, "reasoning_text"},
		{"completed text", `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`, "visible_text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.payload)
			require.Equal(t, tc.want, ResponseContentKind(raw, gjson.GetBytes(raw, "type").String()))
		})
	}
}
