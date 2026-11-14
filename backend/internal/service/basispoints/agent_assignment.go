package basispoints

import "fmt"

func encryptedAgentPath(item object, index int) string {
	if value, exists := item["encrypted_content"]; exists && value != nil && value != "" {
		return fmt.Sprintf("input[%d].encrypted_content", index)
	}
	parts, _ := item["content"].([]any)
	for partIndex, raw := range parts {
		part, _ := raw.(object)
		if text(part["type"]) == "encrypted_content" {
			return fmt.Sprintf("input[%d].content[%d]", index, partIndex)
		}
	}
	return ""
}

// A trailing assignment is the current task. Historical omission is not a
// substitute for its unreadable payload, even if a readable header is present.
func validateNewAgentMessage(input any) error {
	items, _ := input.([]any)
	index := lastMeaningfulInput(items)
	if index < 0 {
		return nil
	}
	item, _ := items[index].(object)
	if text(item["type"]) == "agent_message" {
		if path := encryptedAgentPath(item, index); path != "" {
			return &ContentValidationError{Path: path, ContentType: "encrypted_content", message: "basispoints cannot read a new encrypted agent assignment; resend the task using the original plaintext collaboration path"}
		}
	}
	return nil
}

// ValidateNewAgentMessage must run before history omission and image compaction
// can consume or transform the original task.
func ValidateNewAgentMessage(raw []byte) error {
	var source object
	if decode(raw, &source) != nil || source == nil {
		return fmt.Errorf("invalid Basispoints request JSON")
	}
	return validateNewAgentMessage(source["input"])
}

// Remove only the display annotation for plaintext collaboration. Keep the
// original schema in tool.Parameters/Catalog for validation and cache identity.
func plaintextPromptParameters(name, namespace string, parameters object) object {
	if namespace != "collaboration" || (name != "spawn_agent" && name != "send_message" && name != "followup_task") || parameters == nil {
		return parameters
	}
	properties, _ := parameters["properties"].(object)
	message, _ := properties["message"].(object)
	if message == nil {
		return parameters
	}
	result := make(object, len(parameters))
	for k, v := range parameters {
		result[k] = v
	}
	props := make(object, len(properties))
	for k, v := range properties {
		props[k] = v
	}
	schema := make(object, len(message))
	for k, v := range message {
		if k != "encrypted" {
			schema[k] = v
		}
	}
	props["message"] = schema
	result["properties"] = props
	return result
}

// Metadata and reasoning tails do not turn the current assignment into history.
func lastMeaningfulInput(items []any) int {
	for index := len(items) - 1; index >= 0; index-- {
		item, _ := items[index].(object)
		switch text(item["type"]) {
		case "additional_tools", "tool_search_output", "compaction_trigger", "reasoning":
			continue
		}
		return index
	}
	return -1
}
