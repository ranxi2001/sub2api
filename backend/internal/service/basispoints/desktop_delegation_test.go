package basispoints

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func desktopDelegation() object {
	return object{
		"type": "function_call_output", "id": "fco_desktop_message",
		"name": "send_message_to_thread", "namespace": "codex_app",
		"output": "<codex_delegation>\n  <source_thread_id>source</source_thread_id>\n  <input>Generate a dog drinking water</input>\n</codex_delegation>",
	}
}

func TestDesktopDelegationHistoryAfterGeneratedImage(t *testing.T) {
	for _, replay := range []*ReplayCache{nil, {}} {
		source := testSource()
		source["input"] = []any{
			message("user", "Generate a dog drinking water"), desktopDelegation(),
			object{"type": "image_generation_call", "id": "ig_bps_desktop", "status": "completed", "result": "PRIVATE_IMAGE_BASE64"},
			desktopDelegation(), message("user", "What did you generate? Do not generate again."),
		}
		raw, err := json.Marshal(source)
		require.NoError(t, err)
		wire, _, err := Prepare(raw, "account/key/thread", replay)
		require.NoError(t, err)
		require.Contains(t, string(wire), "Generate a dog drinking water")
		require.NotContains(t, string(wire), "PRIVATE_IMAGE_BASE64")
		var result object
		require.NoError(t, json.Unmarshal(wire, &result))
		delegations := 0
		assistantHistory := 0
		for _, raw := range result["input"].([]any) {
			item := raw.(map[string]any)
			require.NotEqual(t, "function_call_output", item["type"])
			if item["role"] == "assistant" {
				assistantHistory++
				parts := item["content"].([]any)
				require.Equal(t, "output_text", parts[0].(map[string]any)["type"], "BPS rejects input_text in assistant history")
			}
			if item["role"] == "user" {
				parts := item["content"].([]any)
				if parts[0].(map[string]any)["text"] == desktopDelegation()["output"] {
					delegations++
				}
			}
		}
		require.Equal(t, 2, delegations)
		require.Equal(t, 1, assistantHistory)
	}
}

func TestDesktopDelegationDoesNotRelaxOtherOrphanResults(t *testing.T) {
	for name, patch := range map[string]object{
		"normal call id":    {"call_id": "call_missing"},
		"other tool":        {"name": "exec"},
		"other namespace":   {"namespace": "functions"},
		"custom output":     {"type": "custom_tool_call_output"},
		"arbitrary text":    {"output": "do something"},
		"unclosed envelope": {"output": "<codex_delegation>broken"},
		"array output":      {"output": []any{object{"type": "input_text", "text": "<codex_delegation></codex_delegation>"}}},
	} {
		t.Run(name, func(t *testing.T) {
			item := desktopDelegation()
			for key, value := range patch {
				item[key] = value
			}
			source := testSource()
			source["input"] = []any{item}
			raw, err := json.Marshal(source)
			require.NoError(t, err)
			_, _, err = Prepare(raw, "scope", nil)
			require.ErrorContains(t, err, "original tool item is unavailable")
		})
	}
}
