package service

import (
	"bufio"
	"strings"

	"github.com/tidwall/gjson"
)

// Some DeepSeek-compatible upstreams prepend a synthetic empty chunk without the
// blank line separating it from the real assistant-role chunk. SSE clients join
// both data lines, then fail JSON decoding. Only discard that exact first-line
// pair; valid empty chunks, multiline events and other platforms pass through.
// Keep this compatibility path across upgrades until upstream correction has
// been verified with strict per-event JSON parsing; see docs/deepseek-chat-sse.md.
type deepSeekChatSSEPrefixScanner struct {
	scanner *bufio.Scanner
	model   string
	inspect bool
	current string
	pending string
	hasNext bool
}

func newDeepSeekChatSSEPrefixScanner(scanner *bufio.Scanner, account *Account, model string) *deepSeekChatSSEPrefixScanner {
	s := &deepSeekChatSSEPrefixScanner{scanner: scanner, model: model}
	if account == nil || account.Platform != PlatformDeepseek || account.Type != AccountTypeAPIKey || !strings.HasPrefix(strings.ToLower(model), "deepseek-") {
		return s
	}
	s.inspect = true
	return s
}

func (s *deepSeekChatSSEPrefixScanner) Scan() bool {
	if s.hasNext {
		s.current, s.pending, s.hasNext = s.pending, "", false
		return true
	}
	if !s.scanner.Scan() {
		return false
	}
	s.current = s.scanner.Text()
	if !s.inspect {
		return true
	}
	s.inspect = false
	id := deepSeekChatSSESyntheticPrefixID(s.current, s.model)
	if id == "" || !s.scanner.Scan() {
		return true
	}
	next := s.scanner.Text()
	if deepSeekChatSSERealStart(next, s.model, id) {
		s.current = next
		return true
	}
	// A blank line here means the prefix was already its own valid event.
	// Preserve it, and every other unexpected successor, without swallowing EOF
	// or the underlying transport error.
	s.pending, s.hasNext = next, true
	return true
}

func (s *deepSeekChatSSEPrefixScanner) Text() string { return s.current }
func (s *deepSeekChatSSEPrefixScanner) Err() error   { return s.scanner.Err() }

func deepSeekChatSSEChunk(line, model string) (gjson.Result, bool) {
	// The observed preamble is below 512 bytes. Keep recognition bounded even
	// when the application's general SSE line limit permits large tool payloads.
	if len(line) > 4096 {
		return gjson.Result{}, false
	}
	data, ok := extractOpenAISSEDataLine(line)
	if !ok || !gjson.Valid(data) {
		return gjson.Result{}, false
	}
	obj := gjson.Parse(data)
	if !obj.IsObject() || obj.Get("object").String() != "chat.completion.chunk" || obj.Get("model").String() != model || obj.Get("id").Type != gjson.String || obj.Get("id").String() == "" {
		return gjson.Result{}, false
	}
	choices := obj.Get("choices")
	if !choices.IsArray() || len(choices.Array()) != 1 || choices.Get("0.index").Raw != "0" || !choices.Get("0.delta").IsObject() || choices.Get("0.finish_reason").Raw != "null" {
		return gjson.Result{}, false
	}
	return obj, true
}

func deepSeekChatSSESyntheticPrefixID(line, model string) string {
	obj, ok := deepSeekChatSSEChunk(line, model)
	if !ok || len(obj.Map()) != 5 || obj.Get("created").Type != gjson.Number || len(obj.Get("choices.0").Map()) != 3 || len(obj.Get("choices.0.delta").Map()) != 0 {
		return ""
	}
	id := obj.Get("id").String()
	if !strings.HasPrefix(id, "chatcmpl-") {
		return ""
	}
	return id
}

func deepSeekChatSSERealStart(line, model, syntheticID string) bool {
	obj, ok := deepSeekChatSSEChunk(line, model)
	if !ok || obj.Get("id").String() == syntheticID || obj.Get("error").Exists() {
		return false
	}
	delta := obj.Get("choices.0.delta")
	return len(delta.Map()) == 2 && delta.Get("role").String() == "assistant" && delta.Get("content").Type == gjson.String && delta.Get("content").String() == ""
}
