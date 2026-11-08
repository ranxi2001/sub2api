package basispoints

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

func functionCodeBranchTool(combinator string) object {
	properties := object{"code": object{"type": "string"}, "completion_summary": object{"type": "string"}}
	parameters := object{"type": "object", "additionalProperties": false, "properties": properties}
	if combinator != "" {
		parameters[combinator] = []any{
			object{"type": "object", "additionalProperties": false, "properties": object{"code": properties["code"]}, "required": []any{"code"}},
			object{"type": "object", "additionalProperties": false, "properties": object{"completion_summary": properties["completion_summary"]}, "required": []any{"completion_summary"}},
		}
	}
	return object{"type": "function", "name": "branch_tool", "parameters": parameters}
}

func functionCodeBranchOrdinary(t *testing.T, args object) object {
	t.Helper()
	encoded, err := json.Marshal(object{"name": "branch_tool", "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	native := functionCodeTestNative(t, "branch_tool", string(encoded), "Ordinary FUNCTION envelope")
	outer := functionCodeTestArguments(t, native)
	outer["summary"] = "Call client tool branch_tool"
	encoded, err = json.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	native["arguments"] = string(encoded)
	return native
}

func TestFunctionCodeTransportSchemaBranches(t *testing.T) {
	for _, combinator := range []string{"oneOf", "anyOf"} {
		t.Run(combinator, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				code     string
				metadata string
				want     object
			}{
				{"summary_only", "", `{"completion_summary":"Finished task"}`, object{"completion_summary": "Finished task"}},
				{"valid_empty_code", "", `{}`, object{"code": ""}},
				{"exact_code", "  text(\"中文\");\r\n", `{}`, object{"code": "  text(\"中文\");\r\n"}},
				{"nonempty_code_conflict", "must not be removed", `{"completion_summary":"Finished task"}`, nil},
				{"whitespace_code_conflict", " ", `{"completion_summary":"Finished task"}`, nil},
				{"unknown_field", "", `{"completion_summary":"Finished task","undeclared":true}`, nil},
				{"wrong_summary_type", "", `{"completion_summary":7}`, nil},
				{"duplicate_code", "", `{"completion_summary":"Finished task","code":""}`, nil},
				{"number_budget", "", `{"completion_summary":"Finished task","undeclared":1e9999999}`, nil},
			} {
				t.Run(tc.name, func(t *testing.T) {
					source := testSource()
					source["tools"] = []any{functionCodeBranchTool(combinator)}
					_, bridge := mustPrepare(t, source, "scope", nil)
					call, err := bridge.translateCall(functionCodeTestNative(t, "branch_tool", tc.code, tc.metadata))
					if tc.want == nil {
						if err == nil {
							t.Fatal("invalid arguments were accepted")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if got := functionCodeTestArguments(t, call); !reflect.DeepEqual(got, tc.want) {
						t.Fatalf("arguments changed: got %#v, want %#v", got, tc.want)
					}
				})
			}
			for _, args := range []object{{"completion_summary": "Finished task"}, {"code": ""}} {
				source := testSource()
				source["tools"] = []any{functionCodeBranchTool(combinator)}
				_, bridge := mustPrepare(t, source, "scope", nil)
				call, err := bridge.translateCall(functionCodeBranchOrdinary(t, args))
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(functionCodeTestArguments(t, call), args) {
					t.Fatal("ordinary FUNCTION changed the selected branch")
				}
			}
		})
	}
}

func TestFunctionCodeTransportOptionalCodePreservesExplicitEmpty(t *testing.T) {
	source := testSource()
	source["tools"] = []any{functionCodeBranchTool("")}
	_, bridge := mustPrepare(t, source, "scope", nil)
	for _, native := range []object{
		functionCodeTestNative(t, "branch_tool", "", `{"completion_summary":"Finished task"}`),
		functionCodeBranchOrdinary(t, object{"completion_summary": "Finished task"}),
	} {
		call, err := bridge.translateCall(native)
		if err != nil {
			t.Fatal(err)
		}
		got := functionCodeTestArguments(t, call)
		outer := functionCodeTestArguments(t, native)
		_, hasCode := got["code"]
		if hasCode != strings.HasPrefix(text(outer["summary"]), functionCodeTransportPrefix) {
			t.Fatal("optional code presence changed despite a valid original argument object")
		}
	}
}

func TestFunctionCodeBranchCatalogAndReplay(t *testing.T) {
	for _, args := range []object{{"completion_summary": "Finished task"}, {"code": ""}, {"code": "text(\"exact\");\r\n"}} {
		source := testSource()
		source["tools"] = []any{functionCodeBranchTool("oneOf")}
		body, bridge := mustPrepare(t, source, "scope", new(ReplayCache))
		input := mustTestValue[[]any](t, body["input"])
		protocolMessage := mustTestValue[object](t, input[1])
		protocol := text(mustTestValue[object](t, mustTestValue[[]any](t, protocolMessage["content"])[0])["text"])
		for _, want := range []string{"For arguments containing a string code field", "selected valid argument branch omits code", "ordinary FUNCTION envelope", "do not add code", `"oneOf"`} {
			if !strings.Contains(protocol, want) {
				t.Fatalf("catalog lost branch selection rule: %s", want)
			}
		}
		metadata := object{}
		code, hasCode := args["code"].(string)
		if !hasCode {
			metadata["completion_summary"] = args["completion_summary"]
		}
		encoded, _ := json.Marshal(metadata)
		native := functionCodeTestNative(t, "branch_tool", code, string(encoded))
		call, err := bridge.translateCall(native)
		if err != nil {
			t.Fatal(err)
		}
		source["input"] = []any{message("user", "continue"), call, object{"type": "function_call_output", "call_id": "call_code", "output": "recorded result"}}
		for i, cache := range []*ReplayCache{bridge.replay, new(ReplayCache)} {
			next, replayBridge := mustPrepare(t, source, "scope", cache)
			input := mustTestValue[[]any](t, next["input"])
			restored := mustTestValue[object](t, input[len(input)-2])
			if i == 0 && !reflect.DeepEqual(native, restored) {
				t.Fatal("cached native call was not replayed verbatim")
			}
			if i == 1 {
				outer := functionCodeTestArguments(t, restored)
				if strings.HasPrefix(text(outer["summary"]), functionCodeTransportPrefix) != hasCode {
					t.Fatal("rebuilt history chose a transport inconsistent with actual arguments")
				}
			}
			roundTrip, err := replayBridge.translateCall(restored)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(functionCodeTestArguments(t, roundTrip), args) {
				t.Fatal("replayed arguments changed the selected schema branch")
			}
		}
	}
}

func TestFunctionCodeBranchStreamingIsAtomic(t *testing.T) {
	for _, valid := range []bool{true, false} {
		source := testSource()
		source["tools"] = []any{functionCodeBranchTool("oneOf")}
		_, bridge := mustPrepare(t, source, "scope", nil)
		metadata := `{"completion_summary":"Finished task"}`
		if !valid {
			metadata = `{"completion_summary":"Finished task","undeclared":true}`
		}
		native := functionCodeTestNative(t, "branch_tool", "", metadata)
		wire := sse(object{"type": "response.output_item.added", "output_index": 0, "item": native}) +
			sse(object{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": "UNVALIDATED_BRANCH_ARGUMENTS"}) +
			sse(object{"type": "response.output_item.done", "output_index": 0, "item": native}) +
			sse(object{"type": "response.completed", "response": object{"id": "resp_branch", "output": []any{native}}})
		stream := bridge.Stream(io.NopCloser(strings.NewReader(wire)))
		output, err := io.ReadAll(stream)
		_ = stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(output, []byte("UNVALIDATED_BRANCH_ARGUMENTS")) || bytes.Contains(output, []byte(functionCodeTransportPrefix)) {
			t.Fatal("unvalidated native transport leaked downstream")
		}
		calls, completed, failed := 0, 0, 0
		err = readEvents(bytes.NewReader(output), func(_ string, data []byte) error {
			var event object
			if err := decode(data, &event); err != nil {
				return err
			}
			switch event["type"] {
			case "response.output_item.done":
				calls++
				args := functionCodeTestArguments(t, mustTestValue[object](t, event["item"]))
				if !reflect.DeepEqual(args, object{"completion_summary": "Finished task"}) {
					t.Fatal("streaming introduced a forbidden code argument")
				}
			case "response.completed":
				completed++
			case "response.failed":
				failed++
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if valid && (calls != 1 || completed != 1 || failed != 0) {
			t.Fatal("valid branch must produce exactly one complete client tool call")
		}
		if !valid && (calls != 0 || completed != 0 || failed != 1) {
			t.Fatal("invalid metadata must fail without dispatching a client tool call")
		}
	}
}
