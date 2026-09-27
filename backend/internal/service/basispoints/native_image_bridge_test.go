package basispoints

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func nativeImageSource() object {
	source := renderingTestSource()
	source["input"] = []any{message("user", "generate a kitten")}
	ns := source["tools"].([]any)[0].(object)
	ns["tools"].([]any)[0].(object)["description"] = "Run JavaScript. ALL_TOOLS; generatedImage(result: {image_url: string})"
	return source
}

func prepareNativeImageTest(t *testing.T, source object, generate ImageGenerator) ([]byte, *Bridge) {
	t.Helper()
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	updated, bridge, err := PrepareNativeImageBridge(raw, "native/account/1/thread/one", generate)
	require.NoError(t, err)
	return updated, bridge
}

func nativeImageCall() object {
	return object{"type": "function_call", "id": "fc_private", "call_id": "call_private", "name": ImageGenerationToolName, "arguments": `{"prompt":"a kitten drinking water"}`, "status": "completed"}
}

func nativeImageWire(t *testing.T, kind string, payload object) string {
	t.Helper()
	payload["type"] = kind
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return "event: " + kind + "\ndata: " + string(raw) + "\n\n"
}

func TestNativeImageBridgeKeepsNativeRequestAndDelivery(t *testing.T) {
	count := 0
	encoded := renderedTestPNG(t)
	source := nativeImageSource()
	source["metadata"] = object{"large_id": json.Number("9007199254740993")}
	raw, bridge := prepareNativeImageTest(t, source, func(r ImageGenerationRequest) (ImageGenerationResult, error) {
		count++
		require.Equal(t, "a kitten drinking water", r.Prompt)
		return ImageGenerationResult{Item: object{"result": encoded}}, nil
	})
	require.NotNil(t, bridge)
	require.Contains(t, string(raw), ImageGenerationToolName)
	require.Contains(t, string(raw), "9007199254740993")
	require.NotContains(t, string(raw), "run_officejs")
	require.NotContains(t, string(raw), "FUNCTION transport")
	call := nativeImageCall()
	response := object{"id": "resp_native", "status": "completed", "output": []any{call}}
	terminal := nativeImageWire(t, "response.completed", object{"response": response})
	wire := nativeImageWire(t, "response.output_text.delta", object{"delta": "Preparing image"}) +
		nativeImageWire(t, "response.in_progress", object{"response": object{"output": []any{call}}}) +
		nativeImageWire(t, "response.output_item.added", object{"output_index": 0, "item": call}) +
		nativeImageWire(t, "response.function_call_arguments.delta", object{"item_id": "fc_private", "delta": "private arguments"}) +
		nativeImageWire(t, "response.output_item.done", object{"output_index": 0, "item": call}) + terminal + terminal
	body := bridge.NativeImageStream(context.Background(), io.NopCloser(strings.NewReader(wire)))
	out, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, 1, count)
	require.Contains(t, string(out), "Preparing image")
	require.Contains(t, string(out), "sub2api image file delivery v3")
	require.Contains(t, string(out), "response.custom_tool_call_input.done")
	require.NotContains(t, string(out), ImageGenerationToolName)
	require.NotContains(t, string(out), "private arguments")
	var streamedInput, finalInput string
	require.NoError(t, readEvents(strings.NewReader(string(out)), func(_ string, data []byte) error {
		var event object
		if err := decode(data, &event); err != nil {
			return err
		}
		if text(event["type"]) == "response.custom_tool_call_input.delta" {
			streamedInput += text(event["delta"])
		}
		if text(event["type"]) == "response.custom_tool_call_input.done" {
			finalInput = text(event["input"])
		}
		return nil
	}))
	require.Equal(t, finalInput, streamedInput)
}

func TestNativeImageBridgeDefersToClientToolsAndChoices(t *testing.T) {
	for _, mode := range []string{"none", "forced", "hosted", "client", "collision", "no-runtime", "structured", "previous"} {
		t.Run(mode, func(t *testing.T) {
			source := nativeImageSource()
			switch mode {
			case "none":
				source["tool_choice"] = "none"
			case "forced":
				source["tool_choice"] = object{"type": "function", "name": "shell"}
			case "hosted":
				source["tools"] = append(source["tools"].([]any), object{"type": "image_generation"})
			case "client":
				source["tools"] = append(source["tools"].([]any), object{"type": "function", "name": "image_gen.imagegen"})
			case "collision":
				source["tools"] = append(source["tools"].([]any), object{"type": "function", "name": ImageGenerationToolName})
			case "no-runtime":
				delete(source, "tools")
			case "structured":
				source["text"] = object{"format": object{"type": "json_schema"}}
			case "previous":
				source["previous_response_id"] = "resp_previous"
			}
			_, bridge := prepareNativeImageTest(t, source, func(ImageGenerationRequest) (ImageGenerationResult, error) {
				t.Fatal("unexpected generation")
				return ImageGenerationResult{}, nil
			})
			require.Nil(t, bridge)
		})
	}
}

func TestNativeImageBridgeReceiptsAndNextTurn(t *testing.T) {
	encoded := renderedTestPNG(t)
	generate := func(ImageGenerationRequest) (ImageGenerationResult, error) {
		return ImageGenerationResult{Item: object{"result": encoded}}, nil
	}
	_, first := prepareNativeImageTest(t, nativeImageSource(), generate)
	response, err := first.NativeImageJSON([]byte(`{"status":"completed","output":[{"type":"function_call","id":"fc_private","call_id":"call_private","name":"sub2api_generate_image","arguments":"{\"prompt\":\"kitten\"}"}]}`))
	require.NoError(t, err)
	var generated object
	require.NoError(t, decode(response, &generated))
	call := generated["output"].([]any)[0].(object)
	payload, ok := imageFilePayload(call)
	require.True(t, ok)
	png, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	file := "/workspace/output/images/sub2api-" + text(payload["sha256"]) + ".png"
	receipt, _ := json.Marshal(object{"kind": "sub2api_image_file_v1", "status": "saved", "call_id": call["call_id"], "sha256": payload["sha256"], "path": file, "bytes": len(png)})
	source := nativeImageSource()
	source["input"] = append(source["input"].([]any), call, object{"type": "custom_tool_call_output", "call_id": call["call_id"], "output": string(receipt)})
	source["tool_choice"] = "none"
	rawReceipt, receiptBridge := prepareNativeImageTest(t, source, generate)
	require.NotNil(t, receiptBridge)
	require.Nil(t, receiptBridge.imageGenerator)
	require.NotContains(t, string(rawReceipt), ImageGenerationToolName)
	outReceipt, err := receiptBridge.NativeImageJSON([]byte(`{"status":"completed","output":[]}`))
	require.NoError(t, err)
	require.Contains(t, string(outReceipt), file)
	delete(source, "tool_choice")
	for _, nextTurn := range []bool{false, true} {
		if nextTurn {
			source["input"] = append(source["input"].([]any), message("user", "thanks"))
		}
		raw, bridge := prepareNativeImageTest(t, source, generate)
		require.NotContains(t, string(raw), encoded)
		out, err := bridge.NativeImageJSON([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}]}`))
		require.NoError(t, err)
		require.Equal(t, !nextTurn, strings.Contains(string(out), file))
	}
}

func TestNativeImageBridgeLeavesUnrelatedToolsAndFailures(t *testing.T) {
	count := 0
	_, bridge := prepareNativeImageTest(t, nativeImageSource(), func(ImageGenerationRequest) (ImageGenerationResult, error) {
		count++
		return ImageGenerationResult{}, nil
	})
	for _, status := range []string{"failed", "incomplete"} {
		raw, _ := json.Marshal(object{"status": status, "output": []any{nativeImageCall()}})
		out, err := bridge.NativeImageJSON(raw)
		require.NoError(t, err)
		require.NotContains(t, string(out), ImageGenerationToolName)
	}
	tool := object{"type": "function_call", "id": "fc_user", "call_id": "user", "name": "shell", "arguments": "{}"}
	response := object{"status": "completed", "output": []any{tool}}
	wire := nativeImageWire(t, "response.output_item.done", object{"item": tool}) + nativeImageWire(t, "response.completed", object{"response": response})
	body := bridge.NativeImageStream(context.Background(), io.NopCloser(strings.NewReader(wire)))
	out, err := io.ReadAll(body)
	require.NoError(t, err)
	_ = body.Close()
	require.Contains(t, string(out), "fc_user")
	require.NotContains(t, string(out), "image file delivery")
	require.Zero(t, count)
}

type nativeImageTrackedBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *nativeImageTrackedBody) Close() error { b.closed.Store(true); return nil }

func TestNativeImageBridgeClosesParentBeforeGeneration(t *testing.T) {
	parent := &nativeImageTrackedBody{}
	encoded := renderedTestPNG(t)
	_, bridge := prepareNativeImageTest(t, nativeImageSource(), func(ImageGenerationRequest) (ImageGenerationResult, error) {
		if !parent.closed.Load() {
			return ImageGenerationResult{}, errors.New("parent connection still holds its lease")
		}
		return ImageGenerationResult{Item: object{"result": encoded}}, nil
	})
	parent.Reader = strings.NewReader(nativeImageWire(t, "response.completed", object{"response": object{"status": "completed", "output": []any{nativeImageCall()}}}))
	body := bridge.NativeImageStream(context.Background(), parent)
	defer body.Close()
	out, err := io.ReadAll(body)
	require.NoError(t, err)
	require.True(t, parent.closed.Load())
	require.Contains(t, string(out), "sub2api image file delivery v3")
}

func TestNativeImageBridgeCancellationClosesBlockedUpstream(t *testing.T) {
	_, bridge := prepareNativeImageTest(t, nativeImageSource(), func(ImageGenerationRequest) (ImageGenerationResult, error) {
		return ImageGenerationResult{}, errors.New("unexpected generation")
	})
	upstream, upstreamWriter := io.Pipe()
	defer upstreamWriter.Close()
	ctx, cancel := context.WithCancel(context.Background())
	body := bridge.NativeImageStream(ctx, upstream)
	defer body.Close()
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(body); done <- err }()
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("cancelled stream did not close")
	}
}

func TestNativeImageBridgeRejectsDuplicateAndFailedGeneration(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		count := 0
		_, bridge := prepareNativeImageTest(t, nativeImageSource(), func(ImageGenerationRequest) (ImageGenerationResult, error) {
			count++
			return ImageGenerationResult{}, errors.New("upstream rejected image")
		})
		output := []any{nativeImageCall()}
		if duplicate {
			output = append(output, nativeImageCall())
		}
		raw, _ := json.Marshal(object{"status": "completed", "output": output})
		out, err := bridge.NativeImageJSON(raw)
		if duplicate {
			require.Error(t, err)
			require.Zero(t, count)
		} else {
			require.NoError(t, err)
			require.Equal(t, 1, count)
			require.Contains(t, string(out), "native_image_generation_failed")
			require.NotContains(t, string(out), "sub2api image file delivery")
		}
	}
}

func TestNativeImageBridgeClosingStreamCancelsChildGeneration(t *testing.T) {
	childCtx, cancelChild := context.WithCancel(context.Background())
	defer cancelChild()
	started, stopped := make(chan struct{}), make(chan struct{})
	_, bridge := prepareNativeImageTest(t, nativeImageSource(), func(ImageGenerationRequest) (ImageGenerationResult, error) {
		close(started)
		<-childCtx.Done()
		close(stopped)
		return ImageGenerationResult{}, childCtx.Err()
	})
	wire := nativeImageWire(t, "response.completed", object{"response": object{"status": "completed", "output": []any{nativeImageCall()}}})
	body := bridge.NativeImageStream(context.Background(), io.NopCloser(strings.NewReader(wire)), cancelChild)
	defer body.Close()
	drained := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, body); close(drained) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("child did not start")
	}
	require.NoError(t, body.Close())
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("child was not cancelled")
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("stream did not drain")
	}
}
