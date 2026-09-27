package basispoints

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func prepareImageTest(t *testing.T, source object, generate ImageGenerator) (string, *Bridge) {
	t.Helper()
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	wire, bridge, err := PrepareWithImageGeneration(raw, "account/key/thread", nil, generate)
	require.NoError(t, err)
	return string(wire), bridge
}

func imageTestStream(t *testing.T, bridge *Bridge, output []any) (string, object) {
	t.Helper()
	wire := sse(object{"type": "response.completed", "response": object{"id": "resp_test", "status": "completed", "output": output, "usage": object{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12}}})
	body := bridge.Stream(io.NopCloser(strings.NewReader(wire)))
	defer body.Close()
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	var response object
	err = readEvents(strings.NewReader(string(raw)), func(kind string, data []byte) error {
		if kind == "response.completed" || kind == "response.failed" {
			var event object
			if err := decode(data, &event); err != nil {
				return err
			}
			response, _ = event["response"].(object)
		}
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	return string(raw), response
}

func imageTestCall(prompt string) object {
	return nativeCall(object{"name": ImageGenerationToolName, "arguments": object{"prompt": prompt}})
}

func TestServerImageToolOnlyExecutesWhenSelected(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary chat", true: "generate"}[selected], func(t *testing.T) {
			calls := 0
			wire, bridge := prepareImageTest(t, testSource(), func(request ImageGenerationRequest) (ImageGenerationResult, error) {
				calls++
				require.Equal(t, "a kitten drinking water", request.Prompt)
				require.Equal(t, "1024x1024", request.Size)
				return ImageGenerationResult{Item: object{"result": "cG5n", "output_format": "png"}, Usage: object{"input_tokens": json.Number("20"), "output_tokens": json.Number("3"), "total_tokens": json.Number("23")}, ToolUsage: object{"image_gen": object{"output_tokens": 9}}}, nil
			})
			require.Contains(t, wire, ImageGenerationToolName)
			output := []any{object{"type": "message", "role": "assistant", "content": []any{object{"type": "output_text", "text": "hello"}}}}
			if selected {
				output = append(output, imageTestCall("a kitten drinking water"))
			}
			raw, response := imageTestStream(t, bridge, output)
			require.Equal(t, "completed", response["status"])
			require.NotContains(t, raw, ImageGenerationToolName)
			if selected {
				require.Equal(t, 1, calls)
				require.Contains(t, raw, "response.image_generation_call.completed")
				require.Equal(t, json.Number("30"), response["usage"].(object)["input_tokens"])
				item := response["output"].([]any)[1].(object)
				require.Equal(t, "image_generation_call", item["type"])
				require.True(t, strings.HasPrefix(text(item["id"]), generatedImageIDPrefix))
				source := testSource()
				source["input"] = []any{item, message("user", "What did you do?")}
				history, _ := mustPrepare(t, source, "account/key/thread", nil)
				historyRaw, _ := json.Marshal(history)
				require.NotContains(t, string(historyRaw), "cG5n")
				require.Contains(t, string(historyRaw), "earlier server-managed image generation completed")
			} else {
				require.Zero(t, calls)
				require.NotContains(t, raw, "image_generation_call")
			}
		})
	}
}

func TestServerImageToolPreservesClientControls(t *testing.T) {
	for name, patch := range map[string]object{
		"none":              {"tool_choice": "none"},
		"client function":   {"tools": []any{object{"type": "namespace", "name": "image_gen", "tools": []any{object{"type": "function", "name": "imagegen"}}}}},
		"client custom":     {"tools": []any{object{"type": "custom", "name": "image_gen.imagegen"}}},
		"client owned name": {"tools": []any{object{"type": "function", "name": ImageGenerationToolName}}},
		"structured":        {"text": object{"format": object{"type": "json_schema", "name": "test", "schema": object{"type": "object", "properties": object{}, "additionalProperties": false}}}},
	} {
		t.Run(name, func(t *testing.T) {
			source := testSource()
			for key, value := range patch {
				source[key] = value
			}
			_, bridge := prepareImageTest(t, source, func(ImageGenerationRequest) (ImageGenerationResult, error) {
				t.Fatal("unexpected generation")
				return ImageGenerationResult{}, nil
			})
			require.Nil(t, bridge.imageGenerator)
		})
	}
}

func TestServerImageToolRejectsBeforeExecution(t *testing.T) {
	for name, output := range map[string][]any{
		"empty prompt":         {imageTestCall(" ")},
		"unknown field":        {nativeCall(object{"name": ImageGenerationToolName, "arguments": object{"prompt": "cat", "url": "https://private.invalid"}})},
		"two images":           {imageTestCall("one"), imageTestCall("two")},
		"undeclared companion": {imageTestCall("cat"), nativeCall(object{"name": "not_allowed", "arguments": object{}})},
	} {
		t.Run(name, func(t *testing.T) {
			_, bridge := prepareImageTest(t, testSource(), func(ImageGenerationRequest) (ImageGenerationResult, error) {
				t.Fatal("must validate all tools first")
				return ImageGenerationResult{}, nil
			})
			raw, response := imageTestStream(t, bridge, output)
			require.Equal(t, "failed", response["status"])
			require.NotContains(t, raw, "image_generation_call.in_progress")
		})
	}
}

func TestServerImageToolFailureIsPrivateAndRetainsUsage(t *testing.T) {
	_, bridge := prepareImageTest(t, testSource(), func(ImageGenerationRequest) (ImageGenerationResult, error) {
		return ImageGenerationResult{Usage: object{"input_tokens": 5}}, errors.New("PRIVATE_TOKEN_IN_ERROR")
	})
	raw, response := imageTestStream(t, bridge, []any{imageTestCall("cat")})
	require.Equal(t, "failed", response["status"])
	require.NotContains(t, raw, "PRIVATE_TOKEN")
	require.NotContains(t, raw, ImageGenerationToolName)
	require.Equal(t, json.Number("15"), response["usage"].(object)["input_tokens"])
}
