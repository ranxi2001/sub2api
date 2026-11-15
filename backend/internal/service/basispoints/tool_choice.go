package basispoints

import (
	"fmt"
	"strings"
)

type namedToolChoice struct{ kind, name, namespace string }

// Diagnostics contain fixed reasons, never caller-supplied names or values.
type toolChoiceRequestError struct{ reason string }

func (e toolChoiceRequestError) Error() string {
	return "basispoints tool_choice rejected; reason=" + e.reason
}

type toolChoiceViolation struct{ reason string }

func (e toolChoiceViolation) Error() string {
	return "basispoints response violates tool_choice; reason=" + e.reason + "; no tool was executed"
}

func parseToolChoice(value any) (string, *namedToolChoice, error) {
	if value == nil {
		return "auto", nil, nil
	}
	if mode, ok := value.(string); ok {
		switch mode {
		case "auto", "none", "required":
			return mode, nil, nil
		default:
			return "", nil, toolChoiceRequestError{"unsupported_mode"}
		}
	}
	choice, ok := value.(object)
	if !ok {
		return "", nil, toolChoiceRequestError{"invalid_shape"}
	}
	kind := text(choice["type"])
	if kind != "function" && kind != "custom" {
		return "", nil, toolChoiceRequestError{"unsupported_type"}
	}
	name := text(choice["name"])
	if name == "" || name != strings.TrimSpace(name) {
		return "", nil, toolChoiceRequestError{"invalid_name"}
	}
	namespace := ""
	if raw, exists := choice["namespace"]; exists {
		namespace, ok = raw.(string)
		if !ok || namespace == "" || namespace != strings.TrimSpace(namespace) {
			return "", nil, toolChoiceRequestError{"invalid_namespace"}
		}
	}
	for key := range choice {
		if key != "type" && key != "name" && key != "namespace" {
			return "", nil, toolChoiceRequestError{"unsupported_field"}
		}
	}
	return "required", &namedToolChoice{kind: kind, name: name, namespace: namespace}, nil
}

func (b *Bridge) resolveToolChoice(choice *namedToolChoice) error {
	if b.toolChoice == "required" && len(b.tools) == 0 {
		return toolChoiceRequestError{"empty_catalog"}
	}
	if choice == nil {
		return nil
	}
	for key, info := range b.tools {
		if choice.name != key && choice.name != info.Name {
			continue
		}
		if choice.namespace != "" && choice.namespace != info.Namespace {
			continue
		}
		if b.selectedTool != nil {
			return toolChoiceRequestError{"ambiguous_name"}
		}
		selected := info
		b.selectedTool = &selected
	}
	if b.selectedTool == nil {
		return toolChoiceRequestError{"undeclared_target"}
	}
	if b.selectedTool.Kind != choice.kind {
		return toolChoiceRequestError{"type_mismatch"}
	}
	return nil
}

func (b *Bridge) validateSelectedTool(item object) error {
	if b.selectedTool == nil {
		return nil
	}
	target := b.selectedTool
	kind := "function_call"
	if target.Kind == "custom" {
		kind = "custom_tool_call"
	}
	if text(item["type"]) != kind || text(item["name"]) != target.Name || text(item["namespace"]) != target.Namespace {
		return toolChoiceViolation{"wrong_target"}
	}
	return nil
}

func (b *Bridge) toolChoiceInstructions() string {
	if b.selectedTool != nil {
		target := b.selectedTool
		return fmt.Sprintf("\nThis response must call the declared %s tool %s in namespace %s at least once, using its catalog transport and unchanged parameter schema. Do not call any other tool. A text-only response fails this requirement. Other catalog entries remain for history only.", target.Kind, quoted(target.Name), quoted(target.Namespace))
	}
	if b.toolChoice == "required" {
		return "\nThis response must include at least one valid call to a tool declared in the current client catalog. Use its catalog transport and parameter schema. Text or a promise to call a tool does not satisfy tool_choice=required."
	}
	return ""
}
