package basispoints

import (
	"encoding/json"
	"sort"
	"strings"
)

// describeCatalog presents tool contracts as documentation, not native tool definitions.
func describeCatalog(catalog []any) string {
	var lines []string
	for _, raw := range catalog {
		// collectTools constructs every catalog entry as an object.
		entry, _ := raw.(object)
		line := "Client tool " + quoted(entry["name"]) + " (" + text(entry["type"]) + ")."
		if description := text(entry["description"]); description != "" {
			line += " " + description
		}
		if text(entry["type"]) == "custom" {
			line += " Set run_officejs summary to " + quoted("codex2api.custom/"+text(entry["name"])) + " and pass its exact raw text directly in code."
			if format := entry["format"]; format != nil {
				line += " Input format: " + quoted(format) + "."
			}
		} else {
			if supportsFunctionCodeTransport(text(entry["name"]), text(entry["type"]), entry["parameters"]) {
				line += " For arguments containing a string code field, use FUNCTION_CODE transport: set run_officejs summary to " + quoted(functionCodeTransportPrefix+text(entry["name"])) + ". Put the exact code argument directly in native code. Put all other supplied arguments in one JSON object in extended_summary, using only fields declared in the contract; use {} when there are none. Do not include code in that object. If the selected valid argument branch omits code, use the ordinary FUNCTION envelope with a JSON object in its arguments field instead; do not add code, even as an empty string."
			} else if supportsFunctionCmdTransport(text(entry["name"]), text(entry["type"]), entry["parameters"]) {
				line += " Use FUNCTION_CMD transport: set run_officejs summary to " + quoted(functionCmdTransportPrefix+text(entry["name"])) + ". Put the exact cmd argument directly in native code. Put all other supplied arguments in one JSON object in extended_summary, using only fields declared in the contract; use {} when there are none. Do not include cmd in that object."
			} else {
				line += " Pass a JSON object in the envelope's arguments field."
			}
			line += " Argument contract: " + describeSchema(entry["parameters"], 0)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n\n")
}

func quoted(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

const schemaNotation = "Parameter notation: {\"key\":type} requires key; {\"key\"?:type} makes it optional. [schema] is an array. ... allows extra object keys; ...:schema constrains them. Without ... extra keys are forbidden. JSON annotations after a type retain its constraints and descriptions. Complex schemas remain full JSON. Use the exact parameter names and obey every constraint."

// Compact only ordinary schema scaffolding. Validation still uses the original
// JSON Schema; references, unions and unfamiliar dialects are kept as full JSON.
func describeSchema(value any, depth int) string {
	schema, ok := value.(object)
	if !ok || depth >= 8 {
		return quoted(value)
	}
	for _, key := range []string{"$ref", "$dynamicRef", "$defs", "definitions", "allOf", "anyOf", "oneOf", "not", "if", "then", "else", "patternProperties", "dependentSchemas", "prefixItems", "unevaluatedProperties", "unevaluatedItems"} {
		if _, exists := schema[key]; exists {
			return quoted(schema)
		}
	}
	remaining := make(object, len(schema))
	for k, v := range schema {
		if k != "type" {
			remaining[k] = v
		}
	}
	var rendered string
	switch text(schema["type"]) {
	case "object":
		properties := object{}
		if value, exists := schema["properties"]; exists {
			var ok bool
			properties, ok = value.(object)
			if !ok {
				return quoted(schema)
			}
		}
		required := map[string]bool{}
		if raw, exists := schema["required"]; exists {
			names, ok := raw.([]any)
			if !ok {
				return quoted(schema)
			}
			for _, rawName := range names {
				name, ok := rawName.(string)
				if !ok {
					return quoted(schema)
				}
				if _, exists := properties[name]; !exists {
					return quoted(schema)
				}
				required[name] = true
			}
		}
		names := make([]string, 0, len(properties))
		for name := range properties {
			names = append(names, name)
		}
		sort.Strings(names)
		fields := make([]string, 0, len(names)+1)
		for _, name := range names {
			presence := "?"
			if required[name] {
				presence = ""
			}
			fields = append(fields, quoted(name)+presence+":"+describeSchema(properties[name], depth+1))
		}
		additional := any(true)
		if v, exists := schema["additionalProperties"]; exists {
			additional = v
		}
		switch extra := additional.(type) {
		case bool:
			if extra {
				fields = append(fields, "...")
			}
		case object:
			fields = append(fields, "...:"+describeSchema(extra, depth+1))
		default:
			return quoted(schema)
		}
		delete(remaining, "properties")
		delete(remaining, "required")
		delete(remaining, "additionalProperties")
		rendered = "{" + strings.Join(fields, ",") + "}"
	case "array":
		items, exists := schema["items"]
		if !exists {
			return quoted(schema)
		}
		switch items.(type) {
		case object, bool:
		default:
			return quoted(schema)
		}
		rendered = "[" + describeSchema(items, depth+1) + "]"
		delete(remaining, "items")
	case "string", "integer", "number", "boolean", "null":
		rendered = text(schema["type"])
	default:
		return quoted(schema)
	}
	if len(remaining) > 0 {
		rendered += " " + quoted(remaining)
	}
	return rendered
}
