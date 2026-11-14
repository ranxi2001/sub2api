package basispoints

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCurrentEncryptedInputCannotBeOmitted(t *testing.T) {
	for _, kind := range []string{"message", "agent_message", "function_call_output", "custom_tool_call_output"} {
		for _, tail := range []string{"", "reasoning", "additional_tools", "tool_search_output", "compaction_trigger"} {
			t.Run(kind+"/"+tail, func(t *testing.T) {
				field := "content"
				if kind == "function_call_output" || kind == "custom_tool_call_output" {
					field = "output"
				}
				item := object{"type": kind, "role": "user", field: []any{object{"type": "input_text", "text": "Header"}, object{"type": "encrypted_content", "encrypted_content": "PRIVATE_TASK"}}}
				input := []any{message("user", "Previous task"), item}
				if tail != "" {
					input = append(input, object{"type": tail})
				}
				raw, err := json.Marshal(object{"input": input})
				require.NoError(t, err)
				_, err = StripEncryptedContent(raw)
				require.Error(t, err)
				require.Contains(t, err.Error(), "input[1]."+field+"[1]")
				require.NotContains(t, err.Error(), "PRIVATE_TASK")
				// A later plaintext instruction makes the same item historical.
				input = append(input, message("user", "New plaintext task"))
				raw, err = json.Marshal(object{"input": input})
				require.NoError(t, err)
				stripped, err := StripEncryptedContent(raw)
				require.NoError(t, err)
				require.NotContains(t, string(stripped), "PRIVATE_TASK")
				require.Contains(t, string(stripped), encryptedContentOmitted)
			})
		}
	}
}

func TestCompactionPolicyOnlyUsesClientDeclaration(t *testing.T) {
	for _, policy := range []any{[]any{}, []any{object{"type": "compaction", "compact_threshold": 200000}}, []any{object{"type": "compaction", "compact_threshold": 920000}}} {
		source := testSource()
		source["context_management"] = policy
		wire, _ := mustPrepare(t, source, "context", nil)
		want, err := json.Marshal(policy)
		require.NoError(t, err)
		got, err := json.Marshal(wire["context_management"])
		require.NoError(t, err)
		require.JSONEq(t, string(want), string(got))
	}
	source := testSource()
	wire, _ := mustPrepare(t, source, "context", nil)
	require.NotContains(t, wire, "context_management")
}
