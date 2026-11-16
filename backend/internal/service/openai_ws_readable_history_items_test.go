//go:build unit && historyshapes

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSHistoryReadableShapes(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"readable_agent", `{"type":"agent_message","content":[{"type":"input_text","text":"fixture readable delegation 中文"}],"encrypted_content":"fixture-opaque-history"}`, "fixture readable delegation 中文"},
		{"summary_agent", `{"type":"agent_message","summary":[{"type":"summary_text","text":"fixture retained task"}],"encrypted_content":"fixture-opaque-history"}`, "fixture retained task"},
		{"named_task", `{"type":"function_call_output","name":"task","output":"fixture delegated task 中文 9007199254740993"}`, "fixture delegated task 中文 9007199254740993"},
		{"compaction", `{"type":"compaction","summary":"fixture readable progress","encrypted_content":"fixture-opaque-history"}`, "fixture readable progress"},
		{"compaction_summary", `{"type":"compaction_summary","content":[{"type":"summary_text","text":"fixture summary"}],"encrypted_content":"fixture-opaque-history"}`, "fixture summary"},
		{"item_reference", `{"type":"item_reference","id":"fixture-private-reference"}`, openAIHistoricalCiphertextOmitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := openAIWSLocalHistoryBytes.Load()
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			manager, c, a := issue715WSManager(t, svc, context.Background())
			SetOpenAIWSRecoveryPrincipal(c, 715, 715)
			defer func() { manager.abort(); require.Equal(t, before, openAIWSLocalHistoryBytes.Load()) }()
			source := `{"model":"gpt-6-astra","input":[{"role":"user","content":"fixture original task"},` + tc.input + `]}`
			if tc.name == "readable_agent" || tc.name == "summary_agent" {
				// Produce the encrypted envelope through an actual successful
				// delivery first. Readable text never grants an unknown owner.
				require.NoError(t, issue715WSBegin(t, manager, a, `{"model":"gpt-6-astra","input":[{"role":"user","content":"fixture original task"}]}`, "ctx_pool"))
				require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_shape_seed","status":"completed","output":[`+tc.input+`]}}`), func() error { return nil }))
				source = `{"model":"gpt-6-astra","previous_response_id":"resp_shape_seed","input":[{"role":"user","content":"fixture original task"},` + tc.input + `]}`
			}
			require.NoError(t, issue715WSBegin(t, manager, a, source, "ctx_pool"))
			terminal := []byte(`{"type":"response.completed","response":{"id":"resp_shape","status":"completed","output":[{"type":"function_call","call_id":"call_shape","name":"fixture","arguments":"{\"n\":9007199254740993}","status":"completed"}]}}`)
			delivered := 0
			require.NoError(t, manager.write(terminal, func() error { delivered++; return nil }))
			current := []byte(`{"type":"response.create","model":"gpt-6-astra","previous_response_id":"resp_shape","input":[{"type":"function_call_output","call_id":"call_shape","output":"fixture executed once"}]}`)
			cause := openAIWSUnsentTurn(denyOpenAITurn("account_ineligible"), current, "gpt-6-astra", 2)
			got := manager.restoreUnsentHistory(cause)
			var restored *openAIWSRestoredUnsentError
			require.ErrorAs(t, got, &restored, "READABLE_SHAPE_NOT_RESTORED")
			require.Contains(t, string(restored.body), tc.want)
			require.Contains(t, string(restored.body), "fixture executed once")
			require.Contains(t, string(restored.body), "9007199254740993")
			require.NotContains(t, string(restored.body), "fixture-opaque-history")
			require.NotContains(t, string(restored.body), "fixture-private-reference")
			require.NotContains(t, string(restored.body), "previous_response_id")
			require.Equal(t, 1, delivered)
			var body map[string]any
			require.NoError(t, json.Unmarshal(restored.body, &body))
			for _, raw := range body["input"].([]any) {
				require.Contains(t, []any{nil, "message"}, raw.(map[string]any)["type"])
			}
			require.Same(t, cause, manager.restoreUnsentHistory(cause))
		})
	}
}

func TestOpenAIWSHistoryUnsafeShapesRemainRejected(t *testing.T) {
	for name, input := range map[string]string{
		"opaque_agent":              `{"type":"agent_message","encrypted_content":"fixture-unreadable-task"}`,
		"opaque_named_task":         `{"type":"function_call_output","name":"task","output":[{"type":"encrypted_content","encrypted_content":"fixture-unreadable-task"}]}`,
		"named_task_without_result": `{"type":"function_call_output","name":"task"}`,
		"pending_named_task":        `{"type":"function_call_output","name":"task","status":"in_progress","output":"fixture"}`,
		"nameless_result":           `{"type":"function_call_output","output":"fixture"}`,
		"blank_call_id":             `{"type":"function_call_output","call_id":"","name":"task","output":"fixture"}`,
		"unknown_type":              `{"type":"fixture-unknown","text":"fixture"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := openAIWSPlainItems([]byte(`{"input":[` + input + `]}`))
			require.False(t, ok)
		})
	}
	for _, input := range []string{`{"type":"compaction","encrypted_content":"fixture-state"}`, `{"type":"item_reference","id":"fixture-id"}`} {
		items, ok := openAIWSPlainItems([]byte(`{"input":[` + input + `]}`))
		if ok {
			raw, _ := json.Marshal(items)
			require.False(t, openAI409HasReadableIntent(append(append([]byte(`{"input":`), raw...), '}')), "an omitted history marker is not a task")
		}
	}
}
