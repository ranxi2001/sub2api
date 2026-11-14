package basispoints

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func choiceTools() []any {
	return []any{
		object{"type": "function", "name": "lookup", "parameters": object{"type": "object", "properties": object{"key": object{"type": "string"}}, "required": []any{"key"}, "additionalProperties": false}},
		object{"type": "function", "name": "other"},
		object{"type": "namespace", "name": "client", "tools": []any{object{"type": "custom", "name": "execute"}}},
	}
}

func TestToolChoiceAcceptsDeclaredIdentity(t *testing.T) {
	for _, choice := range []any{"required", object{"type": "function", "name": "lookup"}, object{"type": "custom", "name": "client.execute"}, object{"type": "custom", "name": "execute", "namespace": "client"}, object{"type": "custom", "name": "execute"}} {
		t.Run(quoted(choice), func(t *testing.T) {
			source := testSource()
			source["tools"], source["tool_choice"] = choiceTools(), choice
			wire, bridge := mustPrepare(t, source, "choice", nil)
			require.Len(t, bridge.tools, 3, "selection must not prune the shared catalog")
			require.NotContains(t, wire, "tool_choice", "BPS receives instructions, not unsupported native choice")
			call := nativeCall(object{"name": "lookup", "arguments": object{"key": "value"}})
			if bridge.selectedTool != nil && bridge.selectedTool.Kind == "custom" {
				call = repairCall("custom", "codex2api.custom/client.execute", "literal input")
			}
			response := repairResponse("resp", 1, 1, call)
			require.NoError(t, bridge.translateResponse(response))
		})
	}
}

func TestToolChoiceRejectsInvalidSelectionsWithoutEchoingValues(t *testing.T) {
	for _, choice := range []any{false, "private-choice", object{"type": "allowed_tools"}, object{"type": "function", "name": "private-missing"}, object{"type": "function", "name": "lookup", "namespace": "private-namespace"}, object{"type": "custom", "name": "lookup"}, object{"type": "function", "name": "lookup", "namespace": false}, object{"type": "function", "name": "lookup", "extra": "private-value"}} {
		source := testSource()
		source["tools"], source["tool_choice"] = choiceTools(), choice
		raw, err := json.Marshal(source)
		require.NoError(t, err)
		_, _, err = Prepare(raw, "choice", nil)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "private-")
	}
	for _, tools := range []any{nil, []any{object{"type": "web_search"}}} {
		source := testSource()
		source["tools"], source["tool_choice"] = tools, "required"
		raw, _ := json.Marshal(source)
		_, _, err := Prepare(raw, "choice", nil)
		require.ErrorContains(t, err, "empty_catalog")
	}
}

func TestToolChoiceRejectsAmbiguousShortNames(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "namespace", "name": "a", "tools": []any{object{"type": "function", "name": "lookup"}}}, object{"type": "namespace", "name": "b", "tools": []any{object{"type": "function", "name": "lookup"}}}}
	source["tool_choice"] = object{"type": "function", "name": "lookup"}
	raw, _ := json.Marshal(source)
	_, _, err := Prepare(raw, "choice", nil)
	require.ErrorContains(t, err, "ambiguous_name")
	source["tool_choice"] = object{"type": "function", "name": "lookup", "namespace": "b"}
	_, bridge := mustPrepare(t, source, "choice", nil)
	require.Equal(t, "b", bridge.selectedTool.Namespace)
}

func TestToolChoiceFailuresNeverDispatchPartialBatchOrRepairTargets(t *testing.T) {
	for _, failure := range []string{"missing", "wrong", "mixed", "schema", "unknown"} {
		t.Run(failure, func(t *testing.T) {
			source := testSource()
			source["tools"], source["tool_choice"], source["stream"] = choiceTools(), object{"type": "function", "name": "lookup"}, true
			cache := new(ReplayCache)
			_, bridge := mustPrepare(t, source, "choice", cache)
			valid := nativeCall(object{"name": "lookup", "arguments": object{"key": "value"}})
			wrong := repairCall("wrong", "Call", `{"name":"other","arguments":{}}`)
			items := []any{}
			switch failure {
			case "wrong":
				items = []any{wrong}
			case "mixed":
				items = []any{valid, wrong}
			case "schema":
				items = []any{nativeCall(object{"name": "lookup", "arguments": object{"key": 1}})}
			case "unknown":
				items = []any{nativeCall(object{"name": "unknown", "arguments": object{}})}
			}
			response := repairResponse("resp", 12, 4, items...)
			upstream := io.NopCloser(strings.NewReader(sse(object{"type": "response.completed", "response": response})))
			calls := 0
			stream := bridge.StreamWithRepairs(context.Background(), upstream, func(context.Context, object, error) (object, error) { calls++; return nil, nil }, func(context.Context) (io.ReadCloser, error) { calls++; return nil, nil })
			events := repairEvents(t, stream)
			require.Zero(t, calls)
			require.Equal(t, "response.failed", events[len(events)-1]["type"])
			for _, event := range events {
				require.NotEqual(t, "response.output_item.added", event["type"])
			}
			require.Nil(t, cache.get("choice", "call_native"))
			require.Nil(t, cache.get("choice", "wrong"))
		})
	}
}

func TestToolChoiceRequiredAndLaterAutoRemainIndependent(t *testing.T) {
	source := testSource()
	source["tools"], source["tool_choice"] = choiceTools(), "required"
	_, required := mustPrepare(t, source, "same", nil)
	require.ErrorContains(t, required.translateResponse(repairResponse("resp", 1, 1)), "missing_call")
	source["tool_choice"] = "auto"
	_, automatic := mustPrepare(t, source, "same", nil)
	require.NoError(t, automatic.translateResponse(repairResponse("resp", 1, 1)))
	source["tool_choice"] = "none"
	_, none := mustPrepare(t, source, "same", nil)
	require.Empty(t, none.tools)
	require.NoError(t, none.translateResponse(repairResponse("resp", 1, 1)))
}
