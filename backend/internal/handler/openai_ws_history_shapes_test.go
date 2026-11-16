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
