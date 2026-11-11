package service

import "strings"

// openAIWSFirstTokenEventFunc applies the configured TTFT mode to WebSocket
// events so they report first-token time on the same basis as SSE streams.
// codex.* frames are WebSocket-only out-of-band events, not model output.
func openAIWSFirstTokenEventFunc(mode string) func(eventType string, message []byte) bool {
	return func(eventType string, message []byte) bool {
		if strings.HasPrefix(strings.TrimSpace(eventType), "codex.") {
			return false
		}
		return openAIStreamDataStartsTTFT(string(message), eventType, false, mode)
	}
}
