package basispoints

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
)

//go:embed image_file_delivery.js
var imageFileDeliveryJS string

// V1 stays unchanged so in-flight and historical receipts remain valid.
// V2 relies on the verified final image embed instead of uploading a preview
// back to the model after saving the same PNG.
//
//go:embed image_file_delivery_v2.js
var imageFileDeliveryV2JS string

// Keep V2 byte-for-byte for receipts already present in conversation history.
//
//go:embed image_file_delivery_v3.js
var imageFileDeliveryV3Template string

var imageFileDeliveryV3JS = strings.Replace(imageFileDeliveryV3Template, "/* V2_WRITER */", imageFileDeliveryV2JS, 1)

const imageFileDeliveryPrefix = "// sub2api image file delivery v1\nawait ("
const imageFileDeliveryV2Prefix = "// sub2api image file delivery v2\nawait ("
const imageFileDeliveryV3Prefix = "// sub2api image file delivery v3\nawait ("
const imageFileDeliveryHint = "\nGenerated images are saved in the current client task workspace by a declared client tool. A successful sub2api_image_file_v1 receipt confirms file persistence, not user-visible rendering. The gateway attaches the verified local image to the final answer. If file delivery fails, retry the same completed image's delivery call at most once; never regenerate it, invent a path, or claim success without a verified receipt."

func imageFileDeliveryCode(payload object) string {
	raw, _ := json.Marshal(payload)
	return imageFileDeliveryV3Prefix + imageFileDeliveryV3JS + ")(" + string(raw) + ");"
}

func imageFilePayload(item object) (object, bool) {
	name := text(item["name"])
	if ns := text(item["namespace"]); ns != "" {
		name = ns + "." + name
	}
	if text(item["type"]) != "custom_tool_call" || name != "functions.exec" || !strings.HasPrefix(text(item["call_id"]), imageRenderingCallPrefix) {
		return nil, false
	}
	code := text(item["input"])
	prefix := ""
	for _, candidate := range []string{imageFileDeliveryV3Prefix + imageFileDeliveryV3JS + ")(", imageFileDeliveryV2Prefix + imageFileDeliveryV2JS + ")(", imageFileDeliveryPrefix + imageFileDeliveryJS + ")("} {
		if strings.HasPrefix(code, candidate) {
			prefix = candidate
			break
		}
	}
	if prefix == "" || !strings.HasSuffix(code, ");") || len(code) > len(prefix)+base64.StdEncoding.EncodedLen(imageRelayMaxImageBytes)+1024 {
		return nil, false
	}
	var payload object
	if decode([]byte(code[len(prefix):len(code)-2]), &payload) != nil || len(payload) != 3 || text(payload["call_id"]) != text(item["call_id"]) {
		return nil, false
	}
	encoded := text(payload["png_b64"])
	if validateRenderedPNG(encoded) != nil {
		return nil, false
	}
	data, _ := base64.StdEncoding.DecodeString(encoded)
	sum := sha256.Sum256(data)
	canonical, err := json.Marshal(payload)
	if err != nil || text(payload["sha256"]) != hex.EncodeToString(sum[:]) || code != prefix+string(canonical)+");" {
		return nil, false
	}
	return payload, true
}

// Receipts are accepted only for this exact generated call in the current user
// turn. A later user/delegation message or final answer cancels old attachments.
func (b *Bridge) collectImageFileReceipts(input []any) {
	pending := map[string]object{}
	for _, raw := range input {
		item, _ := raw.(object)
		if text(item["role"]) == "user" || isDesktopDelegationMessage(item) || (text(item["role"]) == "assistant" && (text(item["phase"]) == "final_answer" || text(item["phase"]) == "final")) {
			pending = map[string]object{}
			b.imageFiles = nil
		}
		if payload, ok := imageFilePayload(item); ok {
			pending[text(item["call_id"])] = payload
		}
		// A long functions.exec invocation can return a pending cell and finish
		// through functions.wait. The client may replay that result as a native
		// function_call_output even though the original image call was custom.
		if text(item["type"]) != "custom_tool_call_output" && text(item["type"]) != "function_call_output" {
			continue
		}
		id := text(item["call_id"])
		payload := pending[id]
		if payload == nil {
			continue
		}
		var blocks []string
		switch output := item["output"].(type) {
		case string:
			blocks = append(blocks, output)
		case []any:
			for _, raw := range output {
				part, _ := raw.(object)
				if text(part["type"]) == "input_text" || text(part["type"]) == "text" {
					blocks = append(blocks, text(part["text"]))
				}
			}
		}
		for _, block := range blocks {
			if len(block) > 16384 {
				continue
			}
			var receipt object
			if decode([]byte(block), &receipt) != nil || text(receipt["kind"]) != "sub2api_image_file_v1" || text(receipt["status"]) != "saved" || text(receipt["call_id"]) != id || text(receipt["sha256"]) != text(payload["sha256"]) {
				continue
			}
			file := text(receipt["path"])
			if !path.IsAbs(file) || path.Clean(file) != file || strings.ContainsAny(file, "\r\n\x00<>\\") || !strings.HasSuffix(file, "/output/images/sub2api-"+text(payload["sha256"])+".png") {
				continue
			}
			data, _ := base64.StdEncoding.DecodeString(text(payload["png_b64"]))
			size, ok := imageUsageNumber(receipt["bytes"])
			if !ok || size != int64(len(data)) {
				continue
			}
			if len(b.imageFiles) < 8 {
				b.imageFiles = append(b.imageFiles, file)
			}
			delete(pending, id)
			break
		}
	}
}

func (b *Bridge) appendImageFiles(response object, emit func(string, object) error) error {
	if len(b.imageFiles) == 0 || b.structured != nil {
		return nil
	}
	output, _ := response["output"].([]any)
	var written strings.Builder
	for _, raw := range output {
		item, _ := raw.(object)
		if isTool(item) || text(item["type"]) == "image_generation_call" {
			return nil
		}
		content, _ := item["content"].([]any)
		for _, raw := range content {
			part, _ := raw.(object)
			written.WriteString(text(part["text"]))
		}
	}
	for _, file := range b.imageFiles {
		markdown := "![生成的图片](<" + file + ">)"
		// Avoid duplicating an already-correct model image embed.
		if strings.Contains(written.String(), "![") && (strings.Contains(written.String(), "]("+file+")") || strings.Contains(written.String(), "](<"+file+">)")) {
			continue
		}
		item := message("assistant", markdown)
		item["id"] = "msg_bps_image_" + fingerprint([]any{b.scope, file})
		item["status"], item["phase"] = "completed", "final_answer"
		if err := emitStructuredMessage(item, len(output), emit); err != nil {
			return err
		}
		output = append(output, item)
	}
	response["output"] = output
	return nil
}
