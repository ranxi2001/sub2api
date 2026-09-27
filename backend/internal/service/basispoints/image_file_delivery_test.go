package basispoints

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func fileDeliveryTestSource() object {
	source := renderingTestSource()
	source["tools"].([]any)[0].(object)["tools"].([]any)[0].(object)["description"] = "Runs JavaScript with ALL_TOOLS, tools, and generatedImage(result: { image_url: string })."
	return source
}

func fileDeliveryFixture(t *testing.T) (object, object, string) {
	return fileDeliveryFixtureForImage(t, "test-image")
}

func fileDeliveryFixtureForImage(t *testing.T, imageID string) (object, object, string) {
	t.Helper()
	encoded := renderedTestPNG(t)
	_, bridge := prepareImageTest(t, fileDeliveryTestSource(), func(ImageGenerationRequest) (ImageGenerationResult, error) { return ImageGenerationResult{}, nil })
	call, err := bridge.imageRenderingCall(imageID, object{"result": encoded})
	require.NoError(t, err)
	payload, ok := imageFilePayload(call)
	require.True(t, ok)
	file := "/workspace/task/output/images/sub2api-" + text(payload["sha256"]) + ".png"
	data, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	receipt := object{"kind": "sub2api_image_file_v1", "status": "saved", "call_id": call["call_id"], "sha256": payload["sha256"], "path": file, "bytes": len(data)}
	return call, receipt, file
}

func receiptResult(t *testing.T, call object, receipt object) object {
	return receiptResultType(t, call, receipt, "custom_tool_call_output")
}

func receiptResultType(t *testing.T, call object, receipt object, kind string) object {
	t.Helper()
	raw, err := json.Marshal(receipt)
	require.NoError(t, err)
	return object{"type": kind, "call_id": call["call_id"], "output": []any{
		object{"type": "input_text", "text": "Script completed\nWall time 1.5 seconds\nOutput:\n"},
		object{"type": "input_text", "text": string(raw)},
	}}
}

func TestImageFileDeliveryUsesDataTransport(t *testing.T) {
	encoded := renderedTestPNG(t)
	count := 0
	wire, bridge := prepareImageTest(t, fileDeliveryTestSource(), func(ImageGenerationRequest) (ImageGenerationResult, error) {
		count++
		return ImageGenerationResult{Item: object{"result": encoded}, Usage: object{"output_tokens": 7}}, nil
	})
	require.True(t, bridge.imageFileDelivery)
	require.Contains(t, wire, "gateway attaches the verified local image")
	stream, response := imageTestStream(t, bridge, []any{imageTestCall("puppy")})
	require.Equal(t, 1, count)
	require.Equal(t, json.Number("9"), response["usage"].(object)["output_tokens"])
	require.NotContains(t, stream, "image_generation_call")
	call := response["output"].([]any)[0].(object)
	payload, ok := imageFilePayload(call)
	require.True(t, ok)
	require.Equal(t, encoded, payload["png_b64"])
	require.Contains(t, text(call["input"]), "tools.write_stdin")
	require.NotContains(t, text(call["input"]), "generatedImage(")
	compact, ok := compactImageRenderingCall(call)
	require.True(t, ok)
	require.NotContains(t, text(compact["input"]), encoded)
	require.Contains(t, text(compact["input"]), "Binary omitted")
}

func TestVerifiedImageReceiptAppendsFinalMarkdownEvents(t *testing.T) {
	call, receipt, file := fileDeliveryFixture(t)
	source := fileDeliveryTestSource()
	source["input"] = []any{message("user", "make an image"), call, receiptResult(t, call, receipt)}
	wire, bridge := mustPrepare(t, source, "account/key/thread", nil)
	require.Equal(t, []string{file}, bridge.imageFiles)
	rawWire, err := json.Marshal(wire)
	require.NoError(t, err)
	require.NotContains(t, string(rawWire), renderedTestPNG(t))
	stream, response := imageTestStream(t, bridge, []any{message("assistant", "Here is your image.")})
	require.Len(t, response["output"], 2)
	require.Contains(t, stream, "response.output_text.delta")
	require.Contains(t, stream, "response.content_part.done")
	item := response["output"].([]any)[1].(object)
	require.Equal(t, "final_answer", item["phase"])
	require.Equal(t, "![生成的图片](<"+file+">)", item["content"].([]any)[0].(object)["text"])
}

func TestVerifiedImageReceiptAcceptsAsyncWaitFunctionOutput(t *testing.T) {
	call, receipt, file := fileDeliveryFixture(t)
	source := fileDeliveryTestSource()
	source["input"] = []any{message("user", "make an image"), call, receiptResultType(t, call, receipt, "function_call_output")}
	_, bridge := mustPrepare(t, source, "account/key/thread", nil)
	require.Equal(t, []string{file}, bridge.imageFiles)
}

func TestAsyncWaitReceiptCannotAttachToAnotherImageCall(t *testing.T) {
	_, receipt, _ := fileDeliveryFixture(t)
	other, _, _ := fileDeliveryFixtureForImage(t, "other-image")
	source := fileDeliveryTestSource()
	source["input"] = []any{message("user", "make an image"), other, receiptResultType(t, other, receipt, "function_call_output")}
	_, bridge := mustPrepare(t, source, "account/key/thread", nil)
	require.Empty(t, bridge.imageFiles)
}

func TestImageReceiptDoesNotLeakIntoLaterTurns(t *testing.T) {
	for _, next := range []object{
		message("user", "thanks"),
		{"type": "function_call_output", "name": "send_message_to_thread", "namespace": "codex_app", "output": "<codex_delegation>new user request</codex_delegation>"},
		{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{}},
	} {
		call, receipt, _ := fileDeliveryFixture(t)
		bridge := &Bridge{}
		bridge.collectImageFileReceipts([]any{call, receiptResult(t, call, receipt), next})
		require.Empty(t, bridge.imageFiles)
	}
}

func TestImageReceiptRejectsMismatches(t *testing.T) {
	for name, field := range map[string]object{
		"failed": {"status": "failed"}, "hash": {"sha256": strings.Repeat("a", 64)},
		"call": {"call_id": "another-call"}, "size": {"bytes": -1},
		"relative": {"path": "output/images/image.png"}, "arbitrary": {"path": "/workspace/private.png"},
		"traversal": {"path": "/workspace/../workspace/output/images/image.png"},
	} {
		t.Run(name, func(t *testing.T) {
			call, receipt, _ := fileDeliveryFixture(t)
			for key, value := range field {
				receipt[key] = value
			}
			bridge := &Bridge{}
			bridge.collectImageFileReceipts([]any{call, receiptResult(t, call, receipt)})
			require.Empty(t, bridge.imageFiles)
		})
	}
	call, receipt, _ := fileDeliveryFixture(t)
	call["input"] = text(call["input"]) + "\ntext('unrelated');"
	bridge := &Bridge{}
	bridge.collectImageFileReceipts([]any{call, receiptResult(t, call, receipt)})
	require.Empty(t, bridge.imageFiles)
	_, ok := compactImageRenderingCall(call)
	require.False(t, ok)
}

func TestImageAttachmentWaitsForFinalAndAvoidsDuplicates(t *testing.T) {
	_, _, file := fileDeliveryFixture(t)
	for _, existing := range []string{"![custom](" + file + ")", "![custom](<" + file + ">)"} {
		b := &Bridge{imageFiles: []string{file}}
		response := object{"output": []any{message("assistant", existing)}}
		require.NoError(t, b.appendImageFiles(response, func(string, object) error { t.Fatal("duplicate image event"); return nil }))
		require.Len(t, response["output"], 1)
	}
	b := &Bridge{imageFiles: []string{file}}
	response := object{"output": []any{object{"type": "custom_tool_call", "name": "exec"}}}
	require.NoError(t, b.appendImageFiles(response, func(string, object) error { t.Fatal("image attached before tools completed"); return nil }))
	require.Len(t, response["output"], 1)
}

// Optional local integration fixture: reuse an existing PNG, never generate one.
func TestExportImageFileDeliveryFixture(t *testing.T) {
	input, output := os.Getenv("BPS_DELIVERY_TEST_PNG"), os.Getenv("BPS_DELIVERY_TEST_OUTPUT")
	if input == "" || output == "" {
		t.Skip("local client integration fixture not requested")
	}
	data, err := os.ReadFile(input)
	require.NoError(t, err)
	_, b := prepareImageTest(t, fileDeliveryTestSource(), func(ImageGenerationRequest) (ImageGenerationResult, error) {
		t.Fatal("must not generate")
		return ImageGenerationResult{}, nil
	})
	call, err := b.imageRenderingCall("existing-png-integration", object{"result": base64.StdEncoding.EncodeToString(data)})
	require.NoError(t, err)
	raw, err := json.Marshal(call)
	require.NoError(t, err)
	f, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = f.Write(raw)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}
