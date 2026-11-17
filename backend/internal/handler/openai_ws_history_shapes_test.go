//go:build unit && unsentdelta && historyshapes

package handler

import "testing"

func TestOpenAIWSReadableHistoryShapeTakeover(t *testing.T) {
	for _, mode := range readableWSModes {
		for _, tc := range []struct{ name, input string }{
			{"named_task", `,{"type":"function_call_output","name":"task","output":"fixture delegated work"}`},
			{"compaction", `,{"type":"compaction","summary":"fixture readable compacted progress","encrypted_content":"fixture-opaque-compaction"}`},
			{"readable_agent", `,{"type":"agent_message","content":[{"type":"input_text","text":"fixture readable delegated progress"}],"encrypted_content":"fixture-opaque-delegation"}`},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) { runUnsentDeltaWithInput(t, mode, 7, "", tc.input) })
		}
	}
}

func TestOpenAIWSClientHistoryTakeover(t *testing.T) {
	for _, mode := range readableWSModes {
		t.Run(mode, func(t *testing.T) {
			runUnsentDeltaWithPolicy(t, mode, 7, "", `,{"type":"function_call","call_id":"fixture-old-client-call","name":"fixture_old_effect","arguments":"{}","status":"completed"},{"type":"function_call_output","call_id":"fixture-old-client-call","output":"client reports effect already applied"}`, true)
		})
	}
}

func TestOpenAIWSLossyHistoryTakeover(t *testing.T) {
	for _, mode := range readableWSModes {
		t.Run(mode+"/verified_tool", func(t *testing.T) {
			runUnsentDeltaWithLossyPolicy(t, mode, 7, "", "", false, true)
		})
		t.Run(mode+"/unknown_tool_refused", func(t *testing.T) {
			runUnsentDeltaWithLossyPolicy(t, mode, 7, "unknown_call", "", false, true)
		})
	}
}
