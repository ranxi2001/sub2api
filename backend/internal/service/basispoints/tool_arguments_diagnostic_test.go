package basispoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolArgumentsDiagnosticRulesAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name, rule, path string
		schema, args     object
	}{
		{"type", "type", "/count", object{"type": "object", "properties": object{"count": object{"type": "integer"}}}, object{"count": "PRIVATE_VALUE"}},
		{"required", "required", "", object{"type": "object", "required": []any{"count"}}, object{}},
		{"extra", "additionalProperties", "", object{"type": "object", "additionalProperties": false}, object{"PRIVATE_DYNAMIC_KEY": "PRIVATE_VALUE"}},
		{"oneOf", "oneOf", "", object{"type": "object", "oneOf": []any{object{"required": []any{"a"}}, object{"required": []any{"b"}}}}, object{}},
		{"dynamic", "type", "/[redacted]", object{"type": "object", "additionalProperties": object{"type": "integer"}}, object{"PRIVATE_DYNAMIC_KEY": "PRIVATE_VALUE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := testSource()
			source["tools"] = []any{object{"type": "function", "name": "fixture_tool", "parameters": tc.schema}}
			_, bridge := mustPrepare(t, source, "diagnostic", nil)
			_, err := bridge.translateCall(nativeCall(object{"name": "fixture_tool", "arguments": tc.args}))
			var diagnostic toolArgumentsSchemaError
			require.ErrorAs(t, err, &diagnostic)
			require.NotNil(t, diagnostic.Diagnostic)
			require.Equal(t, tc.rule, diagnostic.Diagnostic.Rule)
			require.Equal(t, tc.path, diagnostic.Diagnostic.Path)
			require.Equal(t, "fixture_tool", diagnostic.Diagnostic.Tool)
			require.NotContains(t, err.Error(), "PRIVATE_")
			require.Nil(t, errors.Unwrap(err))
		})
	}
}

func TestToolArgumentsDiagnosticStreamAtomicAndNoRepair(t *testing.T) {
	for _, visible := range []bool{false, true} {
		source := testSource()
		source["tools"] = []any{object{"type": "function", "name": "fixture_tool", "parameters": object{"type": "object", "properties": object{"count": object{"type": "integer"}}}}}
		_, bridge := mustPrepare(t, source, "diagnostic", nil)
		call := nativeCall(object{"name": "fixture_tool", "arguments": object{"count": "PRIVATE_VALUE"}})
		wire := sse(object{"type": "response.completed", "response": repairResponse("fixture", 10, 2, call)})
		if visible {
			wire = sse(object{"type": "response.output_text.delta", "delta": "Visible answer"}) + wire
		}
		repairs := 0
		stream := bridge.StreamWithRepairs(context.Background(), io.NopCloser(strings.NewReader(wire)), func(context.Context, object, error) (object, error) {
			repairs++
			return nil, errors.New("unexpected repair")
		}, nil)
		output, err := io.ReadAll(stream)
		require.NoError(t, err)
		require.NoError(t, stream.Close())
		require.Zero(t, repairs)
		require.NotContains(t, string(output), "PRIVATE_VALUE")
		require.NotContains(t, string(output), "response.function_call_arguments")
		failed := 0
		require.NoError(t, readEvents(bytes.NewReader(output), func(_ string, data []byte) error {
			var event object
			require.NoError(t, json.Unmarshal(data, &event))
			if text(event["type"]) == "response.failed" {
				failed++
				failure := ParseBridgeFailure(data)
				require.NotNil(t, failure)
				require.NotNil(t, failure.ToolArguments)
				require.Equal(t, "type", failure.ToolArguments.Rule)
				require.Equal(t, "/count", failure.ToolArguments.Path)
			}
			return nil
		}))
		require.Equal(t, 1, failed)
	}
}

func TestToolArgumentsDiagnosticResourceBudget(t *testing.T) {
	source := testSource()
	source["tools"] = []any{functionCodeBranchTool("")}
	_, bridge := mustPrepare(t, source, "diagnostic", nil)
	_, err := bridge.translateCall(functionCodeTestNative(t, "branch_tool", "", `{"completion_summary":"PRIVATE_VALUE","x":1e9999999}`))
	var diagnostic toolArgumentsSchemaError
	require.ErrorAs(t, err, &diagnostic)
	require.Equal(t, "resource_limit", diagnostic.Diagnostic.Reason)
	require.NotContains(t, err.Error(), "PRIVATE_VALUE")
}
