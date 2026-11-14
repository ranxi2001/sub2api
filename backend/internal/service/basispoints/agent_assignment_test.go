package basispoints

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewEncryptedAssignmentsFailBeforeOmission(t *testing.T) {
	for _, top := range []bool{false, true} {
		for _, tail := range []string{"", "additional_tools", "tool_search_output", "compaction_trigger", "reasoning"} {
			item := object{"type": "agent_message", "content": []any{object{"type": "input_text", "text": "Readable header"}}}
			if top {
				item["encrypted_content"] = "SYNTHETIC_PRIVATE_PAYLOAD"
			} else {
				item["content"] = append(mustTestValue[[]any](t, item["content"]), object{"type": "encrypted_content", "data": "SYNTHETIC_PRIVATE_PAYLOAD"})
			}
			source := testSource()
			input := []any{item}
			if tail != "" {
				input = append(input, object{"type": tail, "tools": []any{}})
			}
			source["input"] = input
			raw, _ := json.Marshal(source)
			for _, check := range []func() error{
				func() error { return ValidateNewAgentMessage(raw) },
				func() error { _, err := StripEncryptedContent(raw); return err },
				func() error { _, _, err := Prepare(raw, "scope", nil); return err },
			} {
				if err := check(); err == nil || !strings.Contains(err.Error(), "encrypted_content") || strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_PAYLOAD") {
					t.Fatalf("unsafe rejection: %v", err)
				}
			}
		}
	}
}

func TestHistoricalTopLevelAgentCiphertextIsOmittedSafely(t *testing.T) {
	source := testSource()
	source["input"] = []any{object{"type": "agent_message", "encrypted_content": "SYNTHETIC_PRIVATE_PAYLOAD", "content": "Old header"}, message("user", "Current task")}
	raw, _ := json.Marshal(source)
	if _, _, err := Prepare(raw, "scope", nil); err == nil {
		t.Fatal("top-level ciphertext must fail when omission is disabled")
	}
	stripped, err := StripEncryptedContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := Prepare(stripped, "scope", nil)
	if err != nil || strings.Contains(string(body), "SYNTHETIC_PRIVATE_PAYLOAD") || !strings.Contains(string(body), encryptedContentOmitted) || !strings.Contains(string(body), "Current task") {
		t.Fatalf("unsafe historical omission: %s %v", body, err)
	}
}

func TestCollaborationPromptHidesOnlyEncryptionAnnotation(t *testing.T) {
	for _, name := range []string{"spawn_agent", "send_message", "followup_task"} {
		source := collaborationSource(name)
		raw, _ := json.Marshal(source)
		body, bridge, err := Prepare(raw, "scope", nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), `\"encrypted\":true`) {
			t.Fatal("plaintext prompt still requests encryption")
		}
		parameters := bridge.tools["collaboration."+name].Parameters
		if mustTestValue[object](t, mustTestValue[object](t, parameters["properties"])["message"])["encrypted"] != true {
			t.Fatal("original validation schema mutated")
		}
		after, _ := json.Marshal(source)
		if string(after) != string(raw) {
			t.Fatal("caller input mutated")
		}
	}
}
