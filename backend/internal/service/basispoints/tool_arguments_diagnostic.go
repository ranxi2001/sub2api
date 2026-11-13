package basispoints

import (
	"errors"
	"strconv"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ToolArgumentsDiagnostic contains identifiers and fixed rule names only.
// Never retain the validation error: its values and schema URLs may be private.
type ToolArgumentsDiagnostic struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
	Path   string `json:"path"`
	Rule   string `json:"rule"`
}

func newToolArgumentsError(info tool, err error) error {
	d := &ToolArgumentsDiagnostic{Tool: safeSchemaName(info.Name), Reason: "resource_limit", Path: "", Rule: "resource_limit"}
	var validation *jsonschema.ValidationError
	if errors.As(err, &validation) {
		d.Reason, d.Rule = "schema_validation", "schema"
		// Follow only transparent wrappers. Keep oneOf/anyOf as the failure
		// instead of presenting one arbitrary alternative as the required schema.
		for depth := 0; validation != nil && depth < 32; depth++ {
			if validation.ErrorKind != nil {
				keywords := validation.ErrorKind.KeywordPath()
				if len(keywords) > 0 {
					d.Rule = safeToolArgumentRule(keywords[0])
					d.Path = declaredArgumentPath(validation.InstanceLocation, info.Parameters)
					break
				}
			}
			if len(validation.Causes) == 0 {
				break
			}
			validation = validation.Causes[0]
		}
	}
	return toolArgumentsSchemaError{Diagnostic: d}
}

func safeToolArgumentRule(rule string) string {
	switch rule {
	case "resource_limit", "type", "required", "additionalProperties", "unevaluatedProperties", "oneOf", "anyOf", "allOf", "not", "enum", "const", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "minLength", "maxLength", "pattern", "format", "minItems", "maxItems", "uniqueItems", "contains", "minContains", "maxContains", "minProperties", "maxProperties", "dependentRequired", "dependentSchemas", "propertyNames":
		return rule
	default:
		return "schema"
	}
}

// Dynamic object keys can contain user content. Expose only explicitly declared
// property names and array indexes; references/combinators remain redacted.
func declaredArgumentPath(parts []string, schema object) string {
	path := make([]string, 0, len(parts))
	for i, part := range parts {
		if i >= 32 {
			path = append(path, "[truncated]")
			break
		}
		properties, _ := schema["properties"].(object)
		if child, exists := properties[part]; exists {
			path = append(path, safeSchemaName(part))
			schema, _ = child.(object)
		} else if schema["type"] == "array" {
			index, err := strconv.ParseUint(part, 10, 32)
			if err != nil || strconv.FormatUint(index, 10) != part {
				path = append(path, "[redacted]")
				break
			}
			path = append(path, part)
			schema, _ = schema["items"].(object)
		} else {
			path = append(path, "[redacted]")
			break
		}
	}
	return schemaPointer(path, true)
}
