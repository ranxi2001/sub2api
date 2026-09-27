package basispoints

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const ImageGenerationToolName = "sub2api_generate_image"
const generatedImageIDPrefix = "ig_bps_"

const imageGenerationInstructions = "\nNative image generation IS available through the declared sub2api_generate_image tool. The proxy executes this server-managed tool on the native Codex channel and returns an actual image to the client. When the user requests a new raster image, call this tool using the FUNCTION transport; do not claim image generation is unavailable just because image_gen is absent, and do not use shell/CLI/API-key workarounds. Generate at most one image per response. This tool creates new images from text; it does not edit reference images. Do not call it for ordinary text, code, SVG, image analysis, or questions merely discussing image generation."

type ImageGenerationRequest struct {
	Prompt  string `json:"prompt"`
	Size    string `json:"size,omitempty"`
	Quality string `json:"quality,omitempty"`
}

type ImageGenerationResult struct {
	Item      map[string]any
	Usage     map[string]any
	ToolUsage map[string]any
}

type ImageGenerator func(ImageGenerationRequest) (ImageGenerationResult, error)

func (b *Bridge) addImageGenerationTool(catalog []any, generate ImageGenerator) []any {
	// Prefer the client's real image tool. Never intercept a client-owned name.
	for name := range b.tools {
		if name == ImageGenerationToolName || name == "image_gen.imagegen" || strings.HasSuffix(name, ".image_gen.imagegen") {
			return catalog
		}
	}
	parameters := object{
		"type": "object", "additionalProperties": false, "required": []string{"prompt"},
		"properties": object{
			"prompt":  object{"type": "string", "minLength": 1, "maxLength": 32000, "description": "A complete description of the one new image requested by the user."},
			"size":    object{"type": "string", "enum": []string{"auto", "1024x1024", "1536x1024", "1024x1536"}},
			"quality": object{"type": "string", "enum": []string{"low", "medium", "high", "auto"}},
		},
	}
	entry := object{"type": "function", "name": ImageGenerationToolName,
		"description": "Generate one new raster image from a text prompt. This server-managed tool returns the generated image directly; it needs no client API key or local script.",
		"parameters":  parameters,
	}
	b.tools[ImageGenerationToolName] = tool{Name: ImageGenerationToolName, Kind: "function", Definition: fingerprint(entry), Parameters: parameters}
	b.imageGenerator = generate
	b.imageRenderer = b.clientImageRenderer(catalog)
	return append(catalog, entry)
}

func (b *Bridge) isServerImageTool(item object) bool {
	return b.imageGenerator != nil && text(item["type"]) == "function_call" &&
		text(item["name"]) == ImageGenerationToolName && text(item["namespace"]) == ""
}

func parseImageGenerationRequest(arguments string) (ImageGenerationRequest, error) {
	var request ImageGenerationRequest
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("invalid image generation arguments")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || strings.TrimSpace(request.Prompt) == "" || len(request.Prompt) > 32000 {
		return request, fmt.Errorf("image generation requires one nonempty prompt of at most 32000 bytes")
	}
	if request.Size == "" {
		request.Size = "1024x1024"
	}
	if request.Quality == "" {
		request.Quality = "medium"
	}
	switch request.Size {
	case "auto", "1024x1024", "1536x1024", "1024x1536":
	default:
		return request, fmt.Errorf("unsupported image size")
	}
	switch request.Quality {
	case "auto", "low", "medium", "high":
	default:
		return request, fmt.Errorf("unsupported image quality")
	}
	return request, nil
}

// executeImageGeneration runs only after every returned tool has passed catalog
// validation. It replaces the private call with standard hosted-image events;
// an unregistered function call must never reach the client.
func (b *Bridge) executeImageGeneration(response object, emit func(string, object) error) error {
	output, _ := response["output"].([]any)
	index := -1
	for i, raw := range output {
		item, _ := raw.(object)
		if b.isServerImageTool(item) {
			if index >= 0 {
				return fmt.Errorf("at most one server image generation is allowed per response")
			}
			index = i
		}
	}
	if index < 0 {
		return nil
	}
	call, _ := output[index].(object)
	request, err := parseImageGenerationRequest(text(call["arguments"]))
	if err != nil {
		return err
	}
	id := generatedImageIDPrefix + fingerprint([]any{b.scope, call["call_id"]})
	item := object{"id": id, "type": "image_generation_call", "status": "in_progress"}
	output[index] = item
	if b.imageRenderer == nil {
		if err := emit("response.output_item.added", object{"output_index": index, "item": item}); err != nil {
			return err
		}
		if err := emit("response.image_generation_call.in_progress", object{"output_index": index, "item_id": id}); err != nil {
			return err
		}
	}
	generated, generationErr := b.imageGenerator(request)
	if usage, ok := response["usage"].(object); ok {
		mergeImageGenerationUsage(usage, generated.Usage)
	} else if generated.Usage != nil {
		response["usage"] = generated.Usage
	}
	if generated.ToolUsage != nil {
		usage, _ := response["tool_usage"].(object)
		if usage == nil {
			usage = object{}
			response["tool_usage"] = usage
		}
		mergeImageGenerationUsage(usage, generated.ToolUsage)
	}
	if generationErr != nil || generated.Item == nil || text(generated.Item["result"]) == "" {
		item["status"] = "failed"
		response["status"] = "failed"
		response["error"] = object{"code": "basispoints_image_generation_failed", "message": "Native image generation failed; no completed image was returned"}
		// Keep observed usage, but do not dispatch other client tools after failure.
		filtered := make([]any, 0, len(output))
		for _, raw := range output {
			candidate, _ := raw.(object)
			if !isTool(candidate) {
				filtered = append(filtered, raw)
			}
		}
		response["output"] = filtered
		return emit("response.output_item.done", object{"output_index": index, "item": item})
	}
	item = generated.Item
	renderCall, err := b.imageRenderingCall(id, item)
	if err != nil {
		return err
	}
	if renderCall != nil {
		// The registered custom tool supplies the desktop-visible media result.
		// Do not also send an invisible hosted item or duplicate its base64.
		output[index] = renderCall
		return nil
	}
	item["id"], item["type"], item["status"] = id, "image_generation_call", "completed"
	output[index] = item
	if err := emit("response.image_generation_call.completed", object{"output_index": index, "item_id": id}); err != nil {
		return err
	}
	return emit("response.output_item.done", object{"output_index": index, "item": item})
}

func mergeImageGenerationUsage(target, addition object) {
	for key, value := range addition {
		if nested, ok := value.(object); ok {
			existing, _ := target[key].(object)
			if existing == nil {
				existing = object{}
				target[key] = existing
			}
			mergeImageGenerationUsage(existing, nested)
		} else if n, ok := imageUsageNumber(value); ok {
			old, _ := imageUsageNumber(target[key])
			target[key] = old + n
		}
	}
}

func imageUsageNumber(value any) (int64, bool) {
	switch v := value.(type) {
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), v >= 0 && v == float64(int64(v))
	default:
		return 0, false
	}
}
