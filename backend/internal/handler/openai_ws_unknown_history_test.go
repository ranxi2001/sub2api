//go:build unit && unsentdelta

package handler

import "testing"

const issue1084AdditionalTools = `{"type":"additional_tools","id":"at_fixture1084","role":"developer","tools":[{"type":"function","name":"fixture_replacement","description":"fixture replacement tool","parameters":{"type":"object","properties":{"ticket":{"type":"integer","enum":[9007199254740993]}},"required":["ticket"],"additionalProperties":false},"strict":true}]}`

// A documented declaration must survive the real B wire and remain callable.
// It is not asserted to be the unknown item from the production incident.
func TestOpenAIWSUnknownHistoryBoundary(t *testing.T) {
	for _, mode := range readableWSModes {
		t.Run(mode+"/supported_declarations_tool_loop", func(t *testing.T) {
			runUnsentDeltaLifecycle(t, mode, 2, "", ","+issue1084AdditionalTools, false, false, true)
		})
		for _, tc := range []struct{ name, input string }{
			{"unknown_content", `,{"type":"fixture-unknown","content":"fixture unsupported readable content"}`},
			{"compaction_control", `,{"type":"compaction_trigger"}`},
			{"uncertain_declarations", `,{"type":"additional_tools","role":"developer","tools":[{"type":"web_search"}]}`},
			{"unknown_declaration_fields", `,{"type":"additional_tools","role":"developer","tools":[],"content":"fixture unsupported content"}`},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				runUnsentDeltaWithInput(t, mode, 2, "unsupported_history", tc.input)
			})
		}
	}
}
