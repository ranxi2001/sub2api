package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiGenerateContentToChatCompletions(t *testing.T) {
	body := []byte(`{
		"systemInstruction": {"parts": [{"text": "be brief"}]},
		"contents": [
			{"role": "user", "parts": [{"text": "hi"}]},
			{"role": "model", "parts": [
				{"text": "calling"},
				{"functionCall": {"name": "lookup", "args": {"q": "weather"}}}
			]},
			{"role": "user", "parts": [
				{"functionResponse": {"name": "lookup", "response": {"content": "sunny"}}},
				{"text": "thanks"}
			]}
		],
		"tools": [{"functionDeclarations": [{"name": "lookup", "description": "look", "parameters": {"type": "object"}}]}],
		"toolConfig": {"functionCallingConfig": {"mode": "ANY"}},
		"generationConfig": {"temperature": 0.2, "maxOutputTokens": 32, "responseMimeType": "application/json"}
	}`)

	got, err := GeminiGenerateContentToChatCompletions("gpt-4o-mini", body, false)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-mini", gjson.GetBytes(got, "model").String())
	require.False(t, gjson.GetBytes(got, "stream").Bool())
	require.Equal(t, "system", gjson.GetBytes(got, "messages.0.role").String())
	require.Equal(t, "be brief", gjson.GetBytes(got, "messages.0.content").String())
	require.Equal(t, "user", gjson.GetBytes(got, "messages.1.role").String())
	require.Equal(t, "assistant", gjson.GetBytes(got, "messages.2.role").String())
	require.Equal(t, "lookup", gjson.GetBytes(got, "messages.2.tool_calls.0.function.name").String())
	require.Equal(t, "tool", gjson.GetBytes(got, "messages.3.role").String())
	require.Equal(t, gjson.GetBytes(got, "messages.2.tool_calls.0.id").String(), gjson.GetBytes(got, "messages.3.tool_call_id").String())
	require.Equal(t, "sunny", gjson.GetBytes(got, "messages.3.content").String())
	require.Equal(t, "thanks", gjson.GetBytes(got, "messages.4.content").String())
	require.Equal(t, "lookup", gjson.GetBytes(got, "tools.0.function.name").String())
	require.Equal(t, "required", gjson.GetBytes(got, "tool_choice").String())
	require.Equal(t, int64(32), gjson.GetBytes(got, "max_tokens").Int())
	require.Equal(t, "json_object", gjson.GetBytes(got, "response_format.type").String())
	require.False(t, gjson.GetBytes(got, "contents").Exists())
}

func TestGeminiGenerateContentToChatCompletionsKeepsVendorModel(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`)
	got, err := GeminiGenerateContentToChatCompletions("deepseek/deepseek-v4-flash", body, true)
	require.NoError(t, err)
	require.Equal(t, "deepseek/deepseek-v4-flash", gjson.GetBytes(got, "model").String())
	require.True(t, gjson.GetBytes(got, "stream").Bool())
	require.True(t, gjson.GetBytes(got, "stream_options.include_usage").Bool())
}

func TestGeminiGenerateContentToChatCompletionsRejectsCachedContent(t *testing.T) {
	body := []byte(`{"cachedContent":"cachedContents/abc","contents":[{"role":"user","parts":[{"text":"x"}]}]}`)
	_, err := GeminiGenerateContentToChatCompletions("gpt-4o", body, false)
	require.Error(t, err)
}

func TestChatCompletionsJSONToGemini(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-1",
		"model":"gpt-4o-mini",
		"choices":[{
			"index":0,
			"message":{"role":"assistant","content":"hello","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"weather\"}"}}]},
			"finish_reason":"tool_calls"
		}],
		"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}
	}`)
	got, err := ChatCompletionsJSONToGemini(body, "fallback")
	require.NoError(t, err)
	require.Equal(t, "hello", gjson.GetBytes(got, "candidates.0.content.parts.0.text").String())
	require.Equal(t, "lookup", gjson.GetBytes(got, "candidates.0.content.parts.1.functionCall.name").String())
	require.Equal(t, "weather", gjson.GetBytes(got, "candidates.0.content.parts.1.functionCall.args.q").String())
	require.Equal(t, "model", gjson.GetBytes(got, "candidates.0.content.role").String())
	require.Equal(t, "STOP", gjson.GetBytes(got, "candidates.0.finishReason").String())
	require.Equal(t, int64(3), gjson.GetBytes(got, "usageMetadata.promptTokenCount").Int())
	require.Equal(t, "gpt-4o-mini", gjson.GetBytes(got, "modelVersion").String())
}

func TestGeminiSSETranslator(t *testing.T) {
	translator := NewGeminiSSETranslator("gpt-4o")
	var out []byte
	out = append(out, translator.Push([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hel\"}}]}\n"))...)
	out = append(out, translator.Push([]byte("\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3},\"model\":\"gpt-4o\"}\n\ndata: [DONE]\n"))...)
	require.Contains(t, string(out), `"text":"hel"`)
	require.Contains(t, string(out), `"text":"lo"`)
	require.Contains(t, string(out), `"finishReason":"STOP"`)
	require.Contains(t, string(out), `"promptTokenCount":1`)
	require.NotContains(t, string(out), "[DONE]")
	require.True(t, json.Valid([]byte(gjson.Get(firstSSEPayload(string(out)), "@this").Raw)))
}

func TestGeminiSSETranslatorForwardsKeepaliveComments(t *testing.T) {
	translator := NewGeminiSSETranslator("gpt-4o")
	out := translator.Push([]byte(": keepalive\n\n"))
	require.Contains(t, string(out), ": keepalive\n\n")
	out = append(out, translator.Push([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n"))...)
	require.Contains(t, string(out), `"text":"hi"`)
}

func TestGeminiSSETranslatorIsolatesToolsByChoice(t *testing.T) {
	translator := NewGeminiSSETranslator("gpt-4o")
	var out []byte
	out = append(out, translator.Push([]byte("data: {\"choices\":["+
		"{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"alpha\",\"arguments\":\"{\\\"a\\\":\"}}]}},"+
		"{\"index\":1,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"beta\",\"arguments\":\"{\\\"b\\\":\"}}]}}"+
		"]}\n"))...)
	out = append(out, translator.Push([]byte("data: {\"choices\":["+
		"{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"1}\"}}]},\"finish_reason\":\"tool_calls\"},"+
		"{\"index\":1,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"2}\"}}]}}"+
		"]}\n"))...)
	out = append(out, translator.Push([]byte("data: {\"choices\":[{\"index\":1,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n"))...)

	payloads := sseDataPayloads(string(out))
	require.Len(t, payloads, 2)
	require.Equal(t, int64(0), gjson.Get(payloads[0], "candidates.0.index").Int())
	require.Equal(t, "alpha", gjson.Get(payloads[0], "candidates.0.content.parts.0.functionCall.name").String())
	require.Equal(t, int64(1), gjson.Get(payloads[0], "candidates.0.content.parts.0.functionCall.args.a").Int())
	require.False(t, gjson.Get(payloads[0], "candidates.1").Exists())
	require.Equal(t, int64(1), gjson.Get(payloads[1], "candidates.0.index").Int())
	require.Equal(t, "beta", gjson.Get(payloads[1], "candidates.0.content.parts.0.functionCall.name").String())
	require.Equal(t, int64(2), gjson.Get(payloads[1], "candidates.0.content.parts.0.functionCall.args.b").Int())
}

func TestGeminiANYFiltersMultipleAllowedFunctionNames(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"tools":[{"functionDeclarations":[
			{"name":"lookup","parameters":{"type":"OBJECT","properties":{"q":{"type":"STRING"}}}},
			{"name":"secret","parameters":{"type":"object"}},
			{"name":"other","parameters":{"type":"object"}}
		]}],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["lookup","other"]}}
	}`)
	got, err := GeminiGenerateContentToChatCompletions("gpt-4o", body, false)
	require.NoError(t, err)
	require.Equal(t, "required", gjson.GetBytes(got, "tool_choice").String())
	require.Equal(t, int64(2), gjson.GetBytes(got, "tools.#").Int())
	require.Equal(t, "lookup", gjson.GetBytes(got, "tools.0.function.name").String())
	require.Equal(t, "other", gjson.GetBytes(got, "tools.1.function.name").String())
	require.NotContains(t, string(got), "secret")
	require.Equal(t, "object", gjson.GetBytes(got, "tools.0.function.parameters.type").String())
	require.Equal(t, "string", gjson.GetBytes(got, "tools.0.function.parameters.properties.q.type").String())
}

func TestGeminiANYDropsToolsOutsideAllowedNames(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"tools":[{"functionDeclarations":[
			{"name":"lookup","parameters":{"type":"object"}},
			{"name":"other","parameters":{"type":"object"}}
		]}],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["lookup"]}}
	}`)
	got, err := GeminiGenerateContentToChatCompletions("gpt-4o", body, false)
	require.NoError(t, err)
	require.Equal(t, "lookup", gjson.GetBytes(got, "tool_choice.function.name").String())
	require.Equal(t, int64(1), gjson.GetBytes(got, "tools.#").Int())
	require.Equal(t, "lookup", gjson.GetBytes(got, "tools.0.function.name").String())
}

func TestGeminiAUTOSingleAllowedNameDoesNotForceCall(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"tools":[{"functionDeclarations":[
			{"name":"lookup","parameters":{"type":"object"}},
			{"name":"other","parameters":{"type":"object"}}
		]}],
		"toolConfig":{"functionCallingConfig":{"mode":"AUTO","allowedFunctionNames":["lookup"]}}
	}`)
	got, err := GeminiGenerateContentToChatCompletions("gpt-4o", body, false)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(got, "tool_choice").Exists())
	require.Equal(t, int64(1), gjson.GetBytes(got, "tools.#").Int())
	require.Equal(t, "lookup", gjson.GetBytes(got, "tools.0.function.name").String())
}

func TestGeminiAllowedNamesMustMatchADeclaredTool(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"tools":[{"functionDeclarations":[{"name":"lookup","parameters":{"type":"object"}}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"AUTO","allowedFunctionNames":["missing"]}}
	}`)
	_, err := GeminiGenerateContentToChatCompletions("gpt-4o", body, false)
	require.Error(t, err)
}

func TestGeminiResponseSchemaConvertsUppercaseTypes(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"generationConfig":{
			"responseMimeType":"application/json",
			"responseSchema":{
				"type":"OBJECT",
				"propertyOrdering":["city"],
				"properties":{
					"city":{"type":"STRING"},
					"n":{"type":"INTEGER","nullable":true},
					"tags":{"type":"ARRAY","items":{"type":"STRING"}}
				},
				"required":["city"]
			}
		}
	}`)
	got, err := GeminiGenerateContentToChatCompletions("gpt-4o", body, false)
	require.NoError(t, err)
	schema := gjson.GetBytes(got, "response_format.json_schema.schema")
	require.Equal(t, "object", schema.Get("type").String())
	require.Equal(t, "string", schema.Get("properties.city.type").String())
	require.Equal(t, "integer", schema.Get("properties.n.type.0").String())
	require.Equal(t, "null", schema.Get("properties.n.type.1").String())
	require.Equal(t, "array", schema.Get("properties.tags.type").String())
	require.Equal(t, "string", schema.Get("properties.tags.items.type").String())
	require.False(t, schema.Get("propertyOrdering").Exists())
	require.False(t, schema.Get("properties.n.nullable").Exists())
}

func TestGeminiResponseSchemaRejectsUnknownType(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"DATE"}}}`)
	_, err := GeminiGenerateContentToChatCompletions("gpt-4o", body, false)
	require.Error(t, err)
}

func TestGeminiSchemaIntegerConstraintsConvertRecursively(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"OBJECT", "minProperties":"0", "maxProperties":"9223372036854775807",
		"properties":{"tags":{
			"type":"ARRAY", "minItems":"1", "maxItems":2,
			"items":{"type":"STRING", "minLength":0, "maxLength":9223372036854775807}
		}}
	}`)
	for _, target := range []string{"response", "tool"} {
		t.Run(target, func(t *testing.T) {
			body := geminiIntegerSchemaRequest(t, target, schema)
			got, err := GeminiGenerateContentToChatCompletions("gpt-4o", body, false)
			require.NoError(t, err)
			path := "response_format.json_schema.schema"
			if target == "tool" {
				path = "tools.0.function.parameters"
			}
			converted := gjson.GetBytes(got, path)
			for key, expected := range map[string]string{
				"minProperties":                   "0",
				"maxProperties":                   "9223372036854775807",
				"properties.tags.minItems":        "1",
				"properties.tags.maxItems":        "2",
				"properties.tags.items.minLength": "0",
				"properties.tags.items.maxLength": "9223372036854775807",
			} {
				value := converted.Get(key)
				require.Equal(t, gjson.Number, value.Type, key)
				require.Equal(t, expected, value.Raw, key)
			}
		})
	}
}

func TestGeminiSchemaIntegerConstraintsRejectInvalidValues(t *testing.T) {
	for _, target := range []string{"response", "tool"} {
		for _, key := range []string{"minItems", "maxItems", "minProperties", "maxProperties", "minLength", "maxLength"} {
			schemaType := "ARRAY"
			switch key {
			case "minProperties", "maxProperties":
				schemaType = "OBJECT"
			case "minLength", "maxLength":
				schemaType = "STRING"
			}
			for _, raw := range []string{`""`, `"abc"`, `"1.5"`, `"-1"`, `"9223372036854775808"`, `-1`, `1.5`, `9223372036854775808`, `true`, `null`, `[]`, `{}`} {
				t.Run(target+"/"+key+"/"+raw, func(t *testing.T) {
					schema := json.RawMessage(`{"type":"OBJECT","properties":{"nested":{"type":"` + schemaType + `","` + key + `":` + raw + `}}}`)
					body := geminiIntegerSchemaRequest(t, target, schema)
					got, err := GeminiGenerateContentToChatCompletions("gpt-4o", body, false)
					require.ErrorContains(t, err, key+": must be a non-negative int64")
					require.Nil(t, got, "invalid schema must fail before an upstream request can be built")
				})
			}
		}
	}
}

func geminiIntegerSchemaRequest(t *testing.T, target string, schema json.RawMessage) []byte {
	t.Helper()
	body := map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
	}
	if target == "tool" {
		body["tools"] = []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": "lookup", "parameters": schema}}}}
	} else {
		body["generationConfig"] = map[string]any{"responseMimeType": "application/json", "responseSchema": schema}
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return encoded
}

func TestGeminiToolSchemaRejectsUnknownType(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"lookup","parameters":{"type":"OBJECT","properties":{"q":{"type":"FOO"}}}}]}]}`)
	_, err := GeminiGenerateContentToChatCompletions("gpt-4o", body, false)
	require.Error(t, err)
}

func TestEstimateGeminiTotalTokens(t *testing.T) {
	tokens, characters := EstimateGeminiTotalTokens([]byte(`{"systemInstruction":{"parts":[{"text":"abcd"}]},"contents":[{"parts":[{"text":"efgh"}]}]}`))
	require.Equal(t, 9, characters)
	require.Equal(t, 3, tokens)
}

func sseDataPayloads(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		rest, ok := strings.CutPrefix(line, "data: ")
		if ok && rest != "" {
			out = append(out, rest)
		}
	}
	return out
}

func firstSSEPayload(body string) string {
	const prefix = "data: "
	start := len(prefix)
	if len(body) < start {
		return ""
	}
	rest := body[start:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '\n' {
			return rest[:i]
		}
	}
	return rest
}
