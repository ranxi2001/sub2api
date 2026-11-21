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

// ValidateChatCompletionsAnthropicMedia checks empty image data URIs before the
// generic Chat→Responses bridge drops them. Other data URIs reach the MIME-aware
// Responses→Anthropic converter unchanged. Non-data URLs keep their legacy policy.
func ValidateChatCompletionsAnthropicMedia(req *ChatCompletionsRequest) error {
	for _, message := range req.Messages {
		var parts []ChatContentPart
		if json.Unmarshal(message.Content, &parts) != nil {
			continue
		}
		for _, part := range parts {
			if part.Type == "image_url" && part.ImageURL != nil && isEmptyBase64DataURI(part.ImageURL.URL) {
				return &AnthropicMediaError{reason: "invalid empty media data URI for Anthropic conversion"}
			}
		}
	}
	return nil
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
