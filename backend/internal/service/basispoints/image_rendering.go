package basispoints

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"strings"
)

const imageRenderingCallPrefix = "call_bps_image_display_"
const imageRenderingPrefix = "generatedImage("
const imageRenderingSuffix = ");"

// Retained verbatim to recognize historical calls from the previous release.
const imageRenderingHint = "Generated image displayed through the client's native media tool. Do not regenerate this completed image."

// A hosted image item can be retained in Codex history without creating any UI
// item. Prefer workspace-file delivery for clients exposing tool orchestration;
// simple media-helper clients retain their original hosted-image behavior.
// Client code is a fixed template plus a validated PNG JSON literal.
func (b *Bridge) clientImageRenderer(catalog []any) *tool {
	for _, raw := range catalog {
		entry, _ := raw.(object)
		if text(entry["name"]) != "functions.exec" || text(entry["type"]) != "custom" {
			continue
		}
		description := text(entry["description"])
		if !strings.Contains(description, "generatedImage(") || !strings.Contains(description, "image_url") {
			continue
		}
		info := b.tools["functions.exec"]
		b.imageFileDelivery = strings.Contains(description, "ALL_TOOLS")
		return &info
	}
	return nil
}

func (b *Bridge) imageRenderingCall(imageID string, item object) (object, error) {
	if b.imageRenderer == nil {
		return nil, nil
	}
	encoded := text(item["result"])
	if err := validateRenderedPNG(encoded); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(object{"image_url": "data:image/png;base64," + encoded, "output_hint": imageRenderingHint})
	if err != nil {
		return nil, err
	}
	info := b.imageRenderer
	id := imageRenderingCallPrefix + fingerprint([]any{b.scope, imageID})
	code := imageRenderingPrefix + string(payload) + imageRenderingSuffix
	if b.imageFileDelivery {
		data, _ := base64.StdEncoding.DecodeString(encoded)
		sum := sha256.Sum256(data)
		code = imageFileDeliveryCode(object{"png_b64": encoded, "sha256": hex.EncodeToString(sum[:]), "call_id": id})
	}
	call := object{
		"type": "custom_tool_call", "id": "ctc_" + fingerprint(id), "call_id": id,
		"name": info.Name, "status": "completed",
		"input": code,
	}
	if info.Namespace != "" {
		call["namespace"] = info.Namespace
	}
	return call, nil
}

func validateRenderedPNG(encoded string) error {
	if len(encoded) > base64.StdEncoding.EncodedLen(imageRelayMaxImageBytes) {
		return fmt.Errorf("generated image exceeds client presentation limit")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) > imageRelayMaxImageBytes || !bytes.HasPrefix(data, []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return fmt.Errorf("generated image is not a valid PNG")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > imageRelayMaxPixels {
		return fmt.Errorf("generated image dimensions are invalid")
	}
	return nil
}

// Recognize only our exact inert JSON-literal presentation call. Replaying
// multi-megabyte base64 as executable-code text wastes context and can make the
// next conversation turn fail. Keep the call/result identity while omitting the
// duplicate bytes, even after the replay cache or service has restarted.
func compactImageRenderingCall(item object) (object, bool) {
	if payload, ok := imageFilePayload(item); ok {
		compact := make(object, len(item))
		for key, value := range item {
			compact[key] = value
		}
		compact["input"] = "// Previously saved a generated PNG to the client workspace. Binary omitted. SHA256: " + text(payload["sha256"]) + ". The following tool receipt records the verified local file path. Do not regenerate."
		return compact, true
	}
	name := text(item["name"])
	if ns := text(item["namespace"]); ns != "" {
		name = ns + "." + name
	}
	if text(item["type"]) != "custom_tool_call" || name != "functions.exec" || !strings.HasPrefix(text(item["call_id"]), imageRenderingCallPrefix) {
		return nil, false
	}
	code := text(item["input"])
	if !strings.HasPrefix(code, imageRenderingPrefix) || !strings.HasSuffix(code, imageRenderingSuffix) || len(code) > base64.StdEncoding.EncodedLen(imageRelayMaxImageBytes)+1024 {
		return nil, false
	}
	var payload object
	if decode([]byte(code[len(imageRenderingPrefix):len(code)-len(imageRenderingSuffix)]), &payload) != nil || len(payload) != 2 || text(payload["output_hint"]) != imageRenderingHint {
		return nil, false
	}
	url := text(payload["image_url"])
	if !strings.HasPrefix(url, "data:image/png;base64,") || validateRenderedPNG(strings.TrimPrefix(url, "data:image/png;base64,")) != nil {
		return nil, false
	}
	canonical, err := json.Marshal(payload)
	if err != nil || code != imageRenderingPrefix+string(canonical)+imageRenderingSuffix {
		return nil, false
	}
	compact := make(object, len(item))
	for key, value := range item {
		compact[key] = value
	}
	compact["input"] = "// Previously invoked generatedImage with the completed PNG. Binary omitted from historical call; the following client tool result records presentation."
	return compact, true
}
