package basispoints

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const nativeImageInstructions = "\nNative image generation is available through the declared sub2api_generate_image function. Call that function directly when the user asks to generate a new raster image. It uses the account's native image_generation channel, without Basispoints. Do not use Python/PIL, SVG, CLI or API-key workarounds instead of the requested image generation. Image skills are prompt guidance; absence of an image_gen tool does not make this declared function unavailable. Generate at most one image per response. Do not call it for ordinary chat, image analysis, or questions discussing image generation. It creates new images from text and does not edit reference images."

// PrepareNativeImageBridge adds a server-owned function to a native Responses
// request. It never prepares a Basispoints payload or changes the selected route.
// Only an explicitly declared desktop media runtime can receive its output.
func PrepareNativeImageBridge(raw []byte, scope string, generate ImageGenerator) ([]byte, *Bridge, error) {
	if generate == nil {
		return raw, nil, nil
	}
	var source object
	if err := decode(raw, &source); err != nil {
		return nil, nil, err
	}
	if source == nil || text(source["previous_response_id"]) != "" {
		return raw, nil, nil
	}
	inject := source["tool_choice"] == nil || text(source["tool_choice"]) == "auto"
	if config, ok := source["text"].(object); ok {
		if format, ok := config["format"].(object); ok && text(format["type"]) != "" && text(format["type"]) != "text" {
			return raw, nil, nil
		}
	}
	b := &Bridge{tools: make(map[string]tool), scope: scope}
	var catalog []any
	clientImageTool := false
	var visit func(any, string)
	visit = func(value any, namespace string) {
		items, _ := value.([]any)
		for _, rawItem := range items {
			entry, _ := rawItem.(object)
			name := text(entry["name"])
			if namespace != "" {
				name = namespace + "." + name
			}
			if text(entry["type"]) == "namespace" {
				visit(entry["tools"], name)
				continue
			}
			if name == ImageGenerationToolName || name == "image_gen.imagegen" || strings.HasSuffix(name, ".image_gen.imagegen") || text(entry["type"]) == "image_generation" {
				clientImageTool = true
			}
			if name == "functions.exec" && text(entry["type"]) == "custom" {
				info := tool{Name: text(entry["name"]), Namespace: namespace, Kind: "custom"}
				b.tools[name] = info
				catalog = append(catalog, object{"name": name, "type": "custom", "description": entry["description"]})
			}
		}
	}
	visit(source["tools"], "")
	input, _ := source["input"].([]any)
	for _, rawItem := range input {
		item, _ := rawItem.(object)
		if text(item["type"]) == "additional_tools" {
			visit(item["tools"], "")
		}
	}
	if clientImageTool || b.clientImageRenderer(catalog) == nil {
		return raw, nil, nil
	}
	b.collectImageFileReceipts(input)
	if !inject && len(b.imageFiles) == 0 {
		return raw, nil, nil
	}
	if inject {
		augmented := b.addImageGenerationTool(catalog, generate)
		if b.imageGenerator == nil {
			return raw, nil, nil
		}
		tools, _ := source["tools"].([]any)
		source["tools"] = append(tools, augmented[len(augmented)-1])
		source["instructions"] = text(source["instructions"]) + nativeImageInstructions
	}
	source["instructions"] = text(source["instructions"]) + imageFileDeliveryHint
	for i, rawItem := range input {
		if item, ok := rawItem.(object); ok {
			if compact, changed := compactImageRenderingCall(item); changed {
				input[i] = compact
			}
		}
	}
	encoded, err := json.Marshal(source)
	return encoded, b, err
}

// NativeImageJSON transforms completed native responses without translating any
// unrelated client tool. The stream variant uses the same terminal operation.
func (b *Bridge) NativeImageJSON(raw []byte) ([]byte, error) {
	var response object
	if err := decode(raw, &response); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("empty native image bridge response")
	}
	if err := b.finishNativeImageResponse(response, func(string, object) error { return nil }); err != nil {
		return nil, err
	}
	return json.Marshal(response)
}

func (b *Bridge) finishNativeImageResponse(response object, emit func(string, object) error) error {
	if status := text(response["status"]); status != "" && status != "completed" {
		output, _ := response["output"].([]any)
		filtered := make([]any, 0, len(output))
		for _, raw := range output {
			item, _ := raw.(object)
			if !b.isServerImageTool(item) {
				filtered = append(filtered, raw)
			}
		}
		response["output"] = filtered
		return nil
	}
	if err := b.executeImageGeneration(response, emit); err != nil {
		return err
	}
	if text(response["status"]) == "failed" {
		if detail, ok := response["error"].(object); ok {
			detail["code"] = "native_image_generation_failed"
		}
		return nil
	}
	return b.appendImageFiles(response, emit)
}

// NativeImageStream preserves native text and client tool events. Only our
// private function is withheld and replaced with the declared delivery tool.
func (b *Bridge) NativeImageStream(ctx context.Context, upstream io.ReadCloser, cancelGeneration ...context.CancelFunc) io.ReadCloser {
	ctx, cancel := context.WithCancel(ctx)
	cancelAll := func() {
		cancel()
		for _, stop := range cancelGeneration {
			if stop != nil {
				stop()
			}
		}
	}
	reader, writer := io.Pipe()
	body := &streamBody{PipeReader: reader, upstream: upstream, cancel: cancelAll}
	bridge := *b
	generate := b.imageGenerator
	if generate != nil {
		bridge.imageGenerator = func(request ImageGenerationRequest) (ImageGenerationResult, error) {
			// Release the parent upstream lease before the native image child call,
			// including accounts with a concurrency limit of one.
			_ = body.closeUpstream()
			if err := ctx.Err(); err != nil {
				return ImageGenerationResult{}, err
			}
			type outcome struct {
				result ImageGenerationResult
				err    error
			}
			finished := make(chan outcome, 1)
			go func() {
				result, err := generate(request)
				finished <- outcome{result, err}
			}()
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case result := <-finished:
					return result.result, result.err
				case <-ctx.Done():
					return ImageGenerationResult{}, ctx.Err()
				case <-ticker.C:
					// Written by the same goroutine as all protocol events. The
					// client tool remains pending until its complete input arrives.
					if _, err := io.WriteString(writer, ": image generation in progress\n\n"); err != nil {
						return ImageGenerationResult{}, err
					}
				}
			}
		}
	}
	go func() {
		defer cancelAll()
		stop := context.AfterFunc(ctx, func() {
			_ = writer.CloseWithError(ctx.Err())
			_ = body.closeUpstream()
		})
		defer stop()
		err := bridge.transformNativeImageStream(upstream, writer)
		_ = body.closeUpstream()
		_ = writer.CloseWithError(err)
	}()
	return body
}

func (b *Bridge) transformNativeImageStream(reader io.Reader, writer io.Writer) error {
	sequence := 0
	privateIDs := make(map[string]bool)
	terminal := false
	emit := func(kind string, payload object) error {
		payload["type"], payload["sequence_number"] = kind, sequence
		sequence++
		raw, err := json.Marshal(payload)
		if err == nil {
			_, err = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", kind, raw)
		}
		return err
	}
	err := readEvents(reader, func(event string, raw []byte) error {
		if string(raw) == "[DONE]" {
			return nil
		}
		var payload object
		if err := decode(raw, &payload); err != nil || payload == nil {
			return fmt.Errorf("invalid native Responses event")
		}
		kind := text(payload["type"])
		if kind == "" {
			kind = event
		}
		item, _ := payload["item"].(object)
		if b.isServerImageTool(item) {
			privateIDs[text(item["id"])] = true
			return nil
		}
		if privateIDs[text(payload["item_id"])] {
			return nil
		}
		response, _ := payload["response"].(object)
		if kind == "response.completed" || kind == "response.done" || kind == "response.failed" || kind == "response.incomplete" {
			if response == nil {
				return fmt.Errorf("native Responses terminal event has no response")
			}
			if kind == "response.failed" || kind == "response.incomplete" {
				response["status"] = strings.TrimPrefix(kind, "response.")
			}
			output, _ := response["output"].([]any)
			indexes := make(map[int]bool)
			for i, rawItem := range output {
				candidate, _ := rawItem.(object)
				if b.isServerImageTool(candidate) {
					indexes[i] = true
					delete(privateIDs, text(candidate["id"]))
				}
			}
			if len(privateIDs) != 0 && (kind == "response.completed" || kind == "response.done") {
				return fmt.Errorf("native Responses omitted its image tool at completion")
			}
			// Commit the registered delivery tool before starting a potentially
			// slow image call. Otherwise text first-output failover can repeat
			// generation while the first paid image is still running.
			if len(indexes) > 1 {
				return fmt.Errorf("at most one server image generation is allowed per response")
			}
			for index := range indexes {
				if status := text(response["status"]); status != "" && status != "completed" {
					continue
				}
				call, _ := output[index].(object)
				if _, err := parseImageGenerationRequest(text(call["arguments"])); err != nil {
					return err
				}
				imageID := generatedImageIDPrefix + fingerprint([]any{b.scope, call["call_id"]})
				id := imageRenderingCallPrefix + fingerprint([]any{b.scope, imageID})
				item := object{"type": "custom_tool_call", "id": "ctc_" + fingerprint(id), "call_id": id, "name": b.imageRenderer.Name, "input": "", "status": "in_progress"}
				if b.imageRenderer.Namespace != "" {
					item["namespace"] = b.imageRenderer.Namespace
				}
				if err := emit("response.output_item.added", object{"output_index": index, "item": item}); err != nil {
					return err
				}
				if err := emit("response.custom_tool_call_input.delta", object{"output_index": index, "item_id": item["id"], "delta": b.nativeImageDeliveryPrefix()}); err != nil {
					return err
				}
			}
			if err := b.finishNativeImageResponse(response, emit); err != nil {
				return err
			}
			output, _ = response["output"].([]any)
			for i, rawItem := range output {
				candidate, _ := rawItem.(object)
				if indexes[i] && text(candidate["type"]) == "custom_tool_call" && text(response["status"]) != "failed" {
					if err := emitNativeImageDelivery(candidate, i, b.nativeImageDeliveryPrefix(), emit); err != nil {
						return err
					}
				}
			}
			if text(response["status"]) == "failed" {
				kind = "response.failed"
			}
			if err := emit(kind, payload); err != nil {
				return err
			}
			terminal = true
			return io.EOF
		}
		// Lifecycle snapshots may repeat the in-progress output. Keep our
		// server-owned function private there as well as in item events.
		if response != nil {
			if output, ok := response["output"].([]any); ok {
				filtered := make([]any, 0, len(output))
				for _, rawItem := range output {
					candidate, _ := rawItem.(object)
					if b.isServerImageTool(candidate) {
						privateIDs[text(candidate["id"])] = true
					} else {
						filtered = append(filtered, rawItem)
					}
				}
				response["output"] = filtered
			}
		}
		return emit(kind, payload)
	})
	if err != nil && err != io.EOF {
		return err
	}
	if !terminal {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (b *Bridge) nativeImageDeliveryPrefix() string {
	if b.imageFileDelivery {
		return imageFileDeliveryV3Prefix
	}
	return imageRenderingPrefix
}

func emitNativeImageDelivery(item object, index int, prefix string, emit func(string, object) error) error {
	id := item["id"]
	if err := emit("response.custom_tool_call_input.delta", object{"output_index": index, "item_id": id, "delta": strings.TrimPrefix(text(item["input"]), prefix)}); err != nil {
		return err
	}
	if err := emit("response.custom_tool_call_input.done", object{"output_index": index, "item_id": id, "input": item["input"]}); err != nil {
		return err
	}
	return emit("response.output_item.done", object{"output_index": index, "item": item})
}
