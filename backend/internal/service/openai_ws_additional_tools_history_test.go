//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWSAdditionalToolsHistory(t *testing.T) {
	const first = `{"type":"additional_tools","id":"at_fixture","role":"developer","tools":[{"type":"function","name":"fixture","parameters":{"type":"object","properties":{"n":{"enum":[9007199254740993]}}},"strict":true}]}`
	const updated = `{"type":"additional_tools","id":null,"role":"developer","tools":[{"type":"function","name":"fixture","description":"updated schema","parameters":{"type":"object","properties":{"n":{"type":"string"}}},"strict":false}]}`
	for _, declaration := range []string{first, `{"type":"additional_tools","role":"developer","tools":[]}`} {
		t.Run(gjson.Get(declaration, "id").String()+"preserved", func(t *testing.T) {
			body := []byte(`{"input":[{"role":"user","content":"fixture assignment"},` + declaration + `,` + updated + `,{"type":"function_call","call_id":"call_fixture","name":"fixture","arguments":"{}","status":"completed"},{"type":"function_call_output","call_id":"call_fixture","output":"fixture completed"}]}`)
			original := append([]byte(nil), body...)
			items, reason := openAIWSReadableHistoryItems(body)
			require.Empty(t, reason, "SUPPORTED_HISTORY_NOT_PRESERVED")
			require.Equal(t, declaration, string(items[1]), "declaration JSON must be preserved")
			require.Equal(t, updated, string(items[2]), "schema updates retain order")
			encoded, err := json.Marshal(items)
			require.NoError(t, err)
			clean, err := projectOpenAIWSCompletedTools(append(append([]byte(`{"input":`), encoded...), '}'))
			require.NoError(t, err, "SUPPORTED_HISTORY_NOT_PRESERVED")
			require.JSONEq(t, declaration, gjson.GetBytes(clean, "input.1").Raw)
			require.JSONEq(t, updated, gjson.GetBytes(clean, "input.2").Raw)
			require.Contains(t, string(clean), "fixture completed")
			require.NotContains(t, string(clean), `"type":"function_call"`)
			require.Equal(t, original, body)
		})
	}
}

func TestOpenAIWSAdditionalToolsUnsafeHistory(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"missing_role", `{"type":"additional_tools","tools":[]}`},
		{"wrong_role", `{"type":"additional_tools","role":"user","tools":[]}`},
		{"null_tools", `{"type":"additional_tools","role":"developer","tools":null}`},
		{"non_array_tools", `{"type":"additional_tools","role":"developer","tools":{}}`},
		{"hosted_tool", `{"type":"additional_tools","role":"developer","tools":[{"type":"web_search"}]}`},
		{"resource", `{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"fixture","container":"fixture"}]}`},
		{"unknown_field", `{"type":"additional_tools","role":"developer","tools":[],"content":"fixture task"}`},
		{"opaque", `{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"fixture","parameters":{"encrypted_content":"fixture"}}]}`},
		{"invalid_id", `{"type":"additional_tools","id":42,"role":"developer","tools":[]}`},
		{"invalid_name", `{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":42}]}`},
		{"invalid_strict", `{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"fixture","strict":"true"}]}`},
		{"unknown_item", `{"type":"fixture-unknown","content":"fixture task"}`},
		{"configuration", `{"type":"configuration_update","reasoning":{"effort":"high"}}`},
		{"compaction_trigger", `{"type":"compaction_trigger"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"input":[{"role":"user","content":"fixture assignment"},` + tc.input + `]}`)
			_, reason := openAIWSReadableHistoryItems(body)
			require.NotEmpty(t, reason)
			_, err := projectOpenAIWSCompletedTools(body)
			require.Error(t, err)
		})
	}
	declarationOnly := []byte(`{"input":[{"type":"additional_tools","role":"developer","tools":[]}]}`)
	require.False(t, openAI409HasReadableIntent(declarationOnly), "declarations do not invent a task")
	orphan := []byte(`{"input":[{"role":"user","content":"fixture task"},{"type":"additional_tools","role":"developer","tools":[]},{"type":"function_call_output","call_id":"call_unknown","output":"fixture"}]}`)
	_, err := projectOpenAIWSCompletedTools(orphan)
	require.Error(t, err, "declarations do not grant tool provenance or pairing")
}
