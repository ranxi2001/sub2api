package apicompat

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

// AnthropicMediaError identifies a client media validation failure. Its message
// contains only fixed text, never a client URI, MIME header or embedded payload.
type AnthropicMediaError struct {
	reason string
}

func (e *AnthropicMediaError) Error() string { return e.reason }

// ValidateChatCompletionsAnthropicMedia validates existing media carriers before
// the generic Chat→Responses bridge can flatten tool/function content arrays.
// Strings are never interpreted as media. Non-data URLs keep their legacy policy.
func ValidateChatCompletionsAnthropicMedia(req *ChatCompletionsRequest) error {
	for _, message := range req.Messages {
		var parts []ChatContentPart
		if json.Unmarshal(message.Content, &parts) != nil {
			continue
		}
		for _, part := range parts {
			uri := ""
			switch {
			case part.Type == "image_url" && part.ImageURL != nil:
				uri = part.ImageURL.URL
			case part.Type == "file" && part.File != nil:
				uri = part.File.FileData
			}
			if _, err := dataURIToAnthropicMediaBlock(uri); err != nil {
				return err
			}
		}
	}
	return nil
}

// PreserveChatCompletionsAnthropicToolMedia restores structured tool output
// after generic conversion. Item order and generated legacy function call IDs
// are unchanged. Only actual content arrays with convertible data URI media are
// replaced; string tool output and the generic bridge remain untouched.
func PreserveChatCompletionsAnthropicToolMedia(chat *ChatCompletionsRequest, responses *ResponsesRequest) error {
	var items []json.RawMessage
	if err := json.Unmarshal(responses.Input, &items); err != nil {
		return err
	}
	next := 0
	changed := false
	for _, message := range chat.Messages {
		if message.Role != "tool" && message.Role != "function" {
			continue
		}
		var wire map[string]json.RawMessage
		for next < len(items) {
			if err := json.Unmarshal(items[next], &wire); err != nil {
				return err
			}
			var itemType string
			if err := json.Unmarshal(wire["type"], &itemType); err != nil {
				return err
			}
			if itemType == "function_call_output" {
				break
			}
			wire = nil
			next++
		}
		if next == len(items) {
			break
		}
		next++
		var parts []ChatContentPart
		if json.Unmarshal(message.Content, &parts) != nil {
			continue
		}
		converted := convertChatContentPartsToResponses(parts)
		hasMedia := false
		for _, part := range converted {
			if part.Type == "input_image" || part.Type == "input_file" {
				block, err := responsesMediaPartToAnthropicBlock(part, ResponsesToAnthropicOptions{StrictMedia: true})
				if err != nil {
					return err
				}
				hasMedia = hasMedia || block != nil
			}
		}
		if hasMedia {
			content, err := json.Marshal(converted)
			if err != nil {
				return err
			}
			// Keep an actual output array on the wire; a JSON-encoded string
			// would be ordinary tool text on the next conversion.
			wire["output"] = content
			items[next-1], err = json.Marshal(wire)
			if err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		input, err := json.Marshal(items)
		if err != nil {
			return err
		}
		responses.Input = input
	}
	return nil
}

func responsesMediaPartToAnthropicBlock(part ResponsesContentPart, opts ResponsesToAnthropicOptions) (*AnthropicContentBlock, error) {
	uri := part.ImageURL
	blockType := "image"
	if part.Type == "input_file" {
		uri, blockType = part.FileData, "document"
	}
	if opts.StrictMedia {
		return dataURIToAnthropicMediaBlock(uri)
	}
	// Preserve the pre-strict intermediary policy for Gemini/Antigravity:
	// carry the declared MIME and base64 bytes without applying Anthropic's
	// allowlist or validating another target protocol's media constraints.
	if !strings.HasPrefix(uri, "data:") {
		return nil, nil
	}
	mediaType, rest, ok := strings.Cut(strings.TrimPrefix(uri, "data:"), ";")
	if !ok || !strings.HasPrefix(rest, "base64,") {
		return nil, nil
	}
	source := &AnthropicImageSource{Type: "base64", MediaType: mediaType, Data: strings.TrimPrefix(rest, "base64,")}
	// Retain the valid plain-text source introduced with the MIME fix. If it
	// cannot be decoded, preserve the legacy carrier for the final target.
	if mediaType == "text/plain" {
		if data, err := base64.StdEncoding.DecodeString(source.Data); err == nil && utf8.Valid(data) {
			blockType, source.Type, source.Data = "document", "text", string(data)
		}
	}
	return &AnthropicContentBlock{Type: blockType, Source: source}, nil
}

// dataURIToAnthropicMediaBlock chooses the block by MIME rather than by the
// OpenAI field name. Only the existing data:<mime>;base64,<data> policy is
// supported; non-data URLs and file IDs remain unconverted, without fetching.
func dataURIToAnthropicMediaBlock(uri string) (*AnthropicContentBlock, error) {
	if !strings.HasPrefix(uri, "data:") {
		return nil, nil
	}
	header, data, ok := strings.Cut(strings.TrimPrefix(uri, "data:"), ";base64,")
	if !ok || header == "" || strings.ContainsAny(header, ";, \t\r\n") || strings.TrimSpace(data) == "" {
		return nil, &AnthropicMediaError{reason: "invalid media data URI for Anthropic conversion; expected data:<media type>;base64,<data>"}
	}
	blockType := "image"
	switch header {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	case "application/pdf", "text/plain":
		blockType = "document"
	default:
		return nil, &AnthropicMediaError{reason: "unsupported media type for Anthropic conversion; supported types are image/png, image/jpeg, image/gif, image/webp, application/pdf and text/plain"}
	}
	source := &AnthropicImageSource{Type: "base64", MediaType: header, Data: data}
	if header == "text/plain" {
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil || !utf8.Valid(decoded) {
			return nil, &AnthropicMediaError{reason: "invalid base64 UTF-8 text document for Anthropic conversion"}
		}
		// Anthropic PlainTextSource takes decoded text, not base64 bytes.
		source.Type, source.Data = "text", string(decoded)
	} else {
		// Validate encoding without allocating another copy of binary media.
		if _, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding, strings.NewReader(data))); err != nil {
			return nil, &AnthropicMediaError{reason: "invalid base64 media data for Anthropic conversion"}
		}
	}
	return &AnthropicContentBlock{Type: blockType, Source: source}, nil
}
