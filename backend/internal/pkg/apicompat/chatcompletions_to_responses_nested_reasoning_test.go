package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func convertChatBodyForReasoningTest(t *testing.T, body string) []byte {
	t.Helper()
	var req ChatCompletionsRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal chat request: %v", err)
	}
	out, err := ChatCompletionsToResponses(&req)
	if err != nil {
		t.Fatalf("ChatCompletionsToResponses: %v", err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal responses request: %v", err)
	}
	return raw
}

func TestChatCompletionsToResponsesReasoningEffortForms(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"nested medium", `{"model":"gpt-6-sol","messages":[{"role":"user","content":"hi"}],"reasoning":{"effort":"medium"}}`, "medium"},
		{"flat high", `{"model":"gpt-6-sol","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`, "high"},
		{"nested wins over flat", `{"model":"gpt-6-sol","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high","reasoning":{"effort":"medium"}}`, "medium"},
		{"empty nested falls back to flat", `{"model":"gpt-6-sol","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low","reasoning":{}}`, "low"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := convertChatBodyForReasoningTest(t, tc.body)
			if got := gjson.GetBytes(raw, "reasoning.effort").String(); got != tc.want {
				t.Fatalf("reasoning.effort = %q, want %q; body=%s", got, tc.want, raw)
			}
			if got := gjson.GetBytes(raw, "reasoning.summary").String(); got != "auto" {
				t.Fatalf("reasoning.summary = %q, want auto; body=%s", got, raw)
			}
		})
	}
}

func TestChatCompletionsToResponsesNoReasoningStaysUnset(t *testing.T) {
	raw := convertChatBodyForReasoningTest(t, `{"model":"gpt-6-sol","messages":[{"role":"user","content":"hi"}]}`)
	if gjson.GetBytes(raw, "reasoning").Exists() {
		t.Fatalf("reasoning should be absent; body=%s", raw)
	}
}

func TestChatCompletionsRequestWithoutNestedReasoningOmitsField(t *testing.T) {
	raw, err := json.Marshal(&ChatCompletionsRequest{Model: "gpt-6-sol", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(raw, "reasoning").Exists() {
		t.Fatalf("chat body must not gain a nested reasoning field; body=%s", raw)
	}
}
