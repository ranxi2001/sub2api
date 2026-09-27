package basispoints

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func renderedTestPNG(t *testing.T) string {
	t.Helper()
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	return base64.StdEncoding.EncodeToString(data.Bytes())
}

func renderingTestSource() object {
	source := testSource()
	source["tools"] = []any{object{"type": "namespace", "name": "functions", "tools": []any{object{
		"type": "custom", "name": "exec",
		"description": "Runs JavaScript with generatedImage(result: { image_url: string; output_hint?: string }): Appends an image-generation result.",
	}}}}
	return source
}

func TestServerImageUsesDeclaredClientMediaHelper(t *testing.T) {
	encoded := renderedTestPNG(t)
	calls := 0
	_, bridge := prepareImageTest(t, renderingTestSource(), func(ImageGenerationRequest) (ImageGenerationResult, error) {
		calls++
		return ImageGenerationResult{Item: object{"result": encoded}}, nil
	})
	raw, response := imageTestStream(t, bridge, []any{imageTestCall("puppy")})
	require.Equal(t, "completed", response["status"])
	require.Equal(t, 1, calls)
	output := response["output"].([]any)
	require.Len(t, output, 1)
	require.NotContains(t, raw, "image_generation_call")
	render := output[0].(object)
	require.Equal(t, "custom_tool_call", render["type"])
	require.Equal(t, "functions", render["namespace"])
	require.Equal(t, "exec", render["name"])
	require.True(t, strings.HasPrefix(text(render["call_id"]), imageRenderingCallPrefix))
	require.Contains(t, raw, "response.custom_tool_call_input.done")
	require.NotContains(t, raw, ImageGenerationToolName)
	code := text(render["input"])
	require.True(t, strings.HasPrefix(code, imageRenderingPrefix))
	require.True(t, strings.HasSuffix(code, imageRenderingSuffix))
	var payload object
	require.NoError(t, json.Unmarshal([]byte(code[len(imageRenderingPrefix):len(code)-len(imageRenderingSuffix)]), &payload))
	require.Equal(t, "data:image/png;base64,"+encoded, payload["image_url"])
	require.Equal(t, imageRenderingHint, payload["output_hint"])
	require.NotContains(t, code, "tools.")

	// A cold replay cache must not expand the base64 into historical JS source.
	historySource := renderingTestSource()
	historySource["input"] = []any{render, object{"type": "custom_tool_call_output", "call_id": render["call_id"], "output": "image presented"}, message("user", "What happened?")}
	history, _ := mustPrepare(t, historySource, "account/key/thread", nil)
	historyRaw, err := json.Marshal(history)
	require.NoError(t, err)
	require.NotContains(t, string(historyRaw), encoded)
	require.Contains(t, string(historyRaw), "image presented")
	require.Contains(t, string(historyRaw), "Binary omitted")
	require.Contains(t, code, encoded, "history must not mutate the persisted original call")
}

func TestImageRendererRequiresExplicitMatchingCapability(t *testing.T) {
	for _, entry := range []object{
		{"type": "custom", "name": "functions.exec", "description": "generic JavaScript"},
		{"type": "custom", "name": "unrelated.exec", "description": "generatedImage(image_url)"},
		{"type": "function", "name": "functions.exec", "description": "generatedImage(image_url)"},
	} {
		source := testSource()
		source["tools"] = []any{entry}
		_, bridge := prepareImageTest(t, source, func(ImageGenerationRequest) (ImageGenerationResult, error) { return ImageGenerationResult{}, nil })
		require.Nil(t, bridge.imageRenderer)
	}
}

func TestImagePresentationRejectsInvalidPNG(t *testing.T) {
	for name, encoded := range map[string]string{
		"invalid base64": "!not-base64",
		"not an image":   base64.StdEncoding.EncodeToString([]byte("not PNG")),
		"truncated PNG":  base64.StdEncoding.EncodeToString([]byte{137, 80, 78, 71, 13, 10, 26, 10}),
		"too large":      strings.Repeat("A", base64.StdEncoding.EncodedLen(imageRelayMaxImageBytes)+4),
	} {
		t.Run(name, func(t *testing.T) { require.Error(t, validateRenderedPNG(encoded)) })
	}
}

func TestImagePresentationNeverCompactsUnrelatedCode(t *testing.T) {
	_, bridge := prepareImageTest(t, renderingTestSource(), func(ImageGenerationRequest) (ImageGenerationResult, error) { return ImageGenerationResult{}, nil })
	render, err := bridge.imageRenderingCall("ig_bps_test", object{"result": renderedTestPNG(t)})
	require.NoError(t, err)
	for name, change := range map[string]object{
		"other call id":   {"call_id": "user-call"},
		"other namespace": {"namespace": "other"},
		"extra code":      {"input": text(render["input"]) + "text(123);"},
		"not strict JSON": {"input": "generatedImage({image_url:evil()});"},
	} {
		t.Run(name, func(t *testing.T) {
			copy := object{}
			for key, value := range render {
				copy[key] = value
			}
			for key, value := range change {
				copy[key] = value
			}
			_, ok := compactImageRenderingCall(copy)
			require.False(t, ok)
		})
	}
}
