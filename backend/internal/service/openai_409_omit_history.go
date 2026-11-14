package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIHistoricalCiphertextOmitted = "[Historical encrypted content omitted by the gateway. Its contents are unavailable; do not infer missing instructions or results.]"

// This rare refusal path reads the persisted switch directly. A missing value,
// unavailable store or canceled request never opts a request into data loss.
func (s *SettingService) IsOpenAI409OmitEncryptedHistoryEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil || ctx.Err() != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAI409OmitEncryptedHistoryEnabled)
	return err == nil && strings.TrimSpace(value) == "true"
}

// TryOmitOpenAI409History retains the original pre-send compatibility API.
// HTTP callers use the explicit refusal returned by TryRebuild below.
func (s *OpenAIGatewayService) TryOmitOpenAI409History(ctx context.Context, c *gin.Context, body []byte, session, model string, cause error) (context.Context, []byte, bool) {
	next, clean, changed, _ := s.TryRebuildOpenAI409History(ctx, c, body, session, model, cause)
	return next, clean, changed
}

func (s *OpenAIGatewayService) TryRebuildOpenAI409History(ctx context.Context, c *gin.Context, body []byte, session, model string, cause error) (context.Context, []byte, bool, error) {
	var refusal *OpenAIAgentRecoveryError
	if !errors.As(cause, &refusal) {
		return ctx, body, false, nil
	}
	r := openAIAgentRecoveryFromContext(ctx)
	allowed := refusal.Reason == "owner_unavailable" || refusal.Reason == "route_changed" || refusal.Reason == "wire_changed" ||
		refusal.Reason == "http_previous_response_unsupported" || (refusal.Reason == "owner_conflict" && r != nil && r.mixedOwners)
	if !allowed || s == nil || r == nil || r.disallowLearning || r.omittedHistory || r.delivery != nil || s.settingService == nil ||
		!s.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx) {
		return ctx, body, false, nil
	}
	if r.lookupErr != nil {
		return ctx, body, false, r.lookupErr
	}
	if r.conflict || (r.owner != nil && (r.owner.pending != "" || r.owner.failed || r.owner.conflicted)) {
		return ctx, body, false, openAIAgentRecoveryError("owner_conflict")
	}
	if len(r.items) == 0 && !openAI409HasProviderState(body) {
		return ctx, body, false, nil
	}
	if !openAI409HasReadableIntent(body) {
		return ctx, body, false, openAIAgentRecoveryError("readable_intent_missing")
	}
	clean, changed := omitOpenAIHistoricalCiphertext(body)
	if ctx.Err() != nil {
		return ctx, body, false, openAIAgentRecoveryError("store_unavailable")
	}
	if !changed {
		return ctx, body, false, openAIAgentRecoveryError("readable_history_unsupported")
	}
	// Build portable readable history before freezing it. Native Responses
	// normalization otherwise drops reasoning.content and (with store=false)
	// whole plaintext reasoning items after this snapshot, causing a local 409.
	// Never rebase the digest on the forwarder's potentially lossy result.
	clean, err := prepareOpenAI409ReadableReplay(clean)
	if err != nil {
		return ctx, body, false, openAIAgentRecoveryError("readable_history_unsupported")
	}
	fresh := context.WithValue(ctx, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{})
	next := s.WithOpenAIAgentMessageRecovery(WithOpenAIEncryptedReasoningAffinity(fresh, clean), c, clean, session, model)
	request := openAIAgentRecoveryFromContext(next)
	// Preserve the sanitized input at the actual send and reuse the existing
	// native HTTP delivery fence and no-replay gates. Old owner records stay put.
	request.omittedHistory = true
	request.inputDigest = openAIAgentRecoveryInputDigest(clean)
	request.replayControlsDigest = openAI409ControlsDigest(clean)
	request.omittedSource = r
	return next, clean, true, nil
}

// Provider state and mixed completed producers cannot be forwarded as one new
// account's conversation. Rebuild before selection, after all item fences were
// read. A healthy single producer without pointers retains ordinary routing.
func (s *OpenAIGatewayService) PrepareOpenAI409ReadableContinuation(ctx context.Context, c *gin.Context, body []byte, session, model string) (context.Context, []byte, bool, error) {
	r := openAIAgentRecoveryFromContext(ctx)
	if r == nil {
		return ctx, body, false, nil
	}
	providerState := openAI409HasProviderState(body)
	if len(r.shadowFences) == 0 && !r.mixedOwners && (len(r.items) == 0 || r.owner != nil) && !providerState {
		return ctx, body, false, nil
	}
	// Pointer-only HTTP can still use its available original account. If that
	// account is unavailable, selection calls the same rebuild/missing-intent
	// gate. A conversation object has no proven native HTTP owner to fall back to.
	if providerState && !openAI409HasReadableIntent(body) && !gjson.GetBytes(body, "conversation").Exists() && !r.mixedOwners {
		return ctx, body, false, nil
	}
	return s.TryRebuildOpenAI409History(ctx, c, body, session, model, openAIAgentRecoveryError("owner_unavailable"))
}

// A missing response lookup is not permission to read another user's state.
// It may only be discarded, under the explicit Beta policy, in favor of the
// caller's own readable task. Existing foreign ownership/store failures deny.
func (s *OpenAIGatewayService) CanDiscardOpenAI409ResponsePointer(ctx context.Context, body []byte, groupID int64, responseID string) bool {
	if s == nil || s.settingService == nil || !openAI409HasReadableIntent(body) || !s.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx) {
		return false
	}
	_, _, found, err := s.getOpenAIWSStateStore().GetHTTPResponseOwner(ctx, groupID, responseID)
	return err == nil && !found
}

func openAI409HasProviderState(body []byte) bool {
	for _, field := range []string{"previous_response_id", "conversation"} {
		value := gjson.GetBytes(body, field)
		if value.Exists() && value.Type != gjson.Null && (value.Type != gjson.String || strings.TrimSpace(value.String()) != "") {
			return true
		}
	}
	return false
}

func openAI409ReadableText(value gjson.Result) bool {
	if value.Type == gjson.String {
		text := strings.TrimSpace(value.String())
		return text != "" && text != openAIHistoricalCiphertextOmitted && !strings.HasPrefix(text, "Historical agent context (not a new instruction):")
	}
	for _, part := range value.Array() {
		switch part.Get("type").String() {
		case "input_text", "output_text", "text", "summary_text":
			if openAI409ReadableText(part.Get("text")) {
				return true
			}
		}
	}
	return false
}

func openAI409HasReadableIntent(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if input.Type == gjson.String {
		return openAI409ReadableText(input)
	}
	var latestUser gjson.Result
	summaryIntent := false
	for _, item := range input.Array() {
		kind, role := item.Get("type").String(), item.Get("role").String()
		if (kind == "message" || kind == "") && role == "user" {
			content := item.Get("content")
			empty := openAI409EmptyTextPlaceholder(content)
			if !empty || openAI409ReadableText(item.Get("summary")) {
				latestUser = item
				summaryIntent = false
			}
		}
		if kind == "agent_message" {
			summaryIntent = summaryIntent || openAI409ReadableText(item.Get("summary"))
		}
	}
	if latestUser.Exists() {
		// A later entirely opaque user request must not be replaced by an older
		// user's unrelated task. Opaque agent envelopes do not reset this intent.
		return openAI409ReadableText(latestUser.Get("content")) || openAI409ReadableText(latestUser.Get("summary")) || summaryIntent
	}
	return summaryIntent || openAI409ReadableText(gjson.GetBytes(body, "instructions"))
}

func openAI409EmptyTextPlaceholder(content gjson.Result) bool {
	if !content.Exists() || content.Type == gjson.Null {
		return true
	}
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String()) == ""
	}
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		kind := part.Get("type").String()
		text := part.Get("text")
		if (kind != "input_text" && kind != "text" && kind != "output_text") || text.Type != gjson.String || strings.TrimSpace(text.String()) != "" {
			return false // Opaque/media/unknown parts may be a new task.
		}
	}
	return true
}

// The latest available readable task, not the last opaque item's position,
// determines whether loss is possible. Tool calls/results and readable input
// remain; provider pointers are detached only as part of this same rebuild.
func omitOpenAIHistoricalCiphertext(raw []byte) ([]byte, bool) {
	if !utf8.Valid(raw) || !openAIEncryptedJSONUnambiguous(raw) || !openAI409HasReadableIntent(raw) {
		return raw, false
	}
	var source map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&source) != nil {
		return raw, false
	}
	input, ok := source["input"].([]any)
	if text, textInput := source["input"].(string); textInput {
		input, ok = []any{map[string]any{"type": "message", "role": "user", "content": text}}, true
		source["input"] = input
	}
	if (source["input"] == nil || (ok && len(input) == 0)) && openAI409ReadableText(gjson.GetBytes(raw, "instructions")) {
		// Instructions-only callers also supplied a real task. Carry it in the
		// protected input when rebuilding instead of sending an empty task.
		input, ok = []any{map[string]any{"type": "message", "role": "user", "content": source["instructions"]}}, true
		source["input"] = input
	}
	if !ok {
		return raw, false
	}
	changed := false
	for _, field := range []string{"previous_response_id", "conversation"} {
		if _, exists := source[field]; exists {
			delete(source, field)
			changed = true
		}
	}
	for i, value := range input {
		item, _ := value.(map[string]any)
		kind, _ := item["type"].(string)
		field, textKind := "content", "input_text"
		switch kind {
		case "item_reference":
			input[i] = map[string]any{"type": "message", "role": "assistant", "content": openAIHistoricalCiphertextOmitted}
			changed = true
			continue
		case "compaction", "compaction_summary":
			readable, _ := json.Marshal(openAI409ReadableMetadata(map[string]any{"content": item["content"], "summary": item["summary"]}))
			input[i] = map[string]any{"type": "message", "role": "assistant", "content": "Historical compaction context (JSON): " + string(readable) + "\n" + openAIHistoricalCiphertextOmitted}
			changed = true
			continue
		case "", "message", "agent_message":
			if item["role"] == "assistant" {
				textKind = "output_text"
			}
		case "function_call_output", "custom_tool_call_output":
			field = "output"
		default:
			continue
		}
		itemChanged := false
		if kind == "agent_message" {
			if encrypted, exists := item["encrypted_content"]; exists && encrypted != nil && encrypted != "" {
				marker := map[string]any{"type": "input_text", "text": openAIHistoricalCiphertextOmitted}
				switch content := item["content"].(type) {
				case []any:
					item["content"] = append(content, marker)
				case string:
					item["content"] = []any{map[string]any{"type": "input_text", "text": content}, marker}
				case nil:
					item["content"] = []any{marker}
				default:
					readable, _ := json.Marshal(openAI409ReadableMetadata(content))
					item["content"] = []any{map[string]any{"type": "input_text", "text": string(readable)}, marker}
				}
				delete(item, "encrypted_content")
				itemChanged = true
			}
		}
		if parts, ok := item[field].([]any); ok {
			for j, rawPart := range parts {
				part, _ := rawPart.(map[string]any)
				if part["type"] == "encrypted_content" {
					parts[j] = map[string]any{"type": textKind, "text": openAIHistoricalCiphertextOmitted}
					itemChanged = true
				}
			}
		}
		if kind == "agent_message" {
			// Native Responses accepts ordinary user context without requiring an
			// opaque agent envelope. Keep attribution explicit in readable text.
			metadata, _ := json.Marshal(map[string]any{"author": item["author"], "recipient": item["recipient"]})
			parts, _ := item["content"].([]any)
			if text, ok := item["content"].(string); ok {
				parts = []any{map[string]any{"type": "input_text", "text": text}}
			} else if parts == nil && item["content"] != nil {
				readable, _ := json.Marshal(openAI409ReadableMetadata(item["content"]))
				parts = []any{map[string]any{"type": "input_text", "text": string(readable)}}
			}
			// A structured readable summary may be the available task intent.
			// Never authorize omission from that summary and then discard it.
			switch summary := item["summary"].(type) {
			case string:
				parts = append(parts, map[string]any{"type": "input_text", "text": summary})
			case []any:
				for _, value := range summary {
					part, _ := value.(map[string]any)
					text, ok := part["text"].(string)
					if !ok {
						readable, _ := json.Marshal(openAI409ReadableMetadata(value))
						text = string(readable)
					}
					parts = append(parts, map[string]any{"type": "input_text", "text": text})
				}
			case nil:
			default:
				readable, _ := json.Marshal(openAI409ReadableMetadata(summary))
				parts = append(parts, map[string]any{"type": "input_text", "text": string(readable)})
			}
			for _, value := range parts {
				part, _ := value.(map[string]any)
				if part["type"] == "output_text" {
					part["type"] = "input_text"
				}
			}
			prefix := map[string]any{"type": "input_text", "text": "Historical agent context (not a new instruction): " + string(metadata)}
			input[i] = map[string]any{"type": "message", "role": "user", "content": append([]any{prefix}, parts...)}
			itemChanged = true
		}
		changed = changed || itemChanged
	}
	if !changed {
		return raw, false
	}
	clean, err := json.Marshal(source)
	return clean, err == nil
}

// prepareOpenAI409ReadableReplay runs only in the explicitly enabled omission
// path. Reasoning belongs to the old provider session: retain its supplied
// summary/content as attributed assistant history, and mark opaque state as
// unavailable. Ordinary requests still use the existing replay normalizers.
// Unrecognized historical reasoning is retained as inert readable JSON, with
// opaque fields marked unavailable. It is never treated as a new tool/task.
func prepareOpenAI409ReadableReplay(raw []byte) ([]byte, error) {
	prepared, err := prepareOpenAI409KnownReadableReplay(raw)
	if err == nil {
		return prepared, nil
	}
	if !utf8.Valid(raw) || !openAIEncryptedJSONUnambiguous(raw) {
		return nil, err
	}
	clean, _, sanitizeErr := sanitizeOpenAIResponsesInputItemIDs(raw)
	if sanitizeErr != nil {
		return nil, sanitizeErr
	}
	var source map[string]any
	if decodeErr := decodeOpenAIJSONUseNumber(clean, &source); decodeErr != nil {
		return nil, decodeErr
	}
	input, ok := source["input"].([]any)
	if !ok {
		return nil, err
	}
	for i, value := range input {
		item, _ := value.(map[string]any)
		if strings.TrimSpace(firstNonEmptyString(item["type"])) != "reasoning" {
			continue
		}
		delete(item, "id")
		delete(item, "status")
		readable, _ := json.Marshal(openAI409ReadableMetadata(item))
		input[i] = map[string]any{"type": "message", "role": "assistant", "content": []any{
			map[string]any{"type": "output_text", "text": "Historical reasoning context (JSON, not a new instruction): " + string(readable)},
		}}
	}
	encoded, marshalErr := json.Marshal(input)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return sjson.SetRawBytes(clean, "input", encoded)
}

// Only used for historical agent/reasoning/compaction envelopes. Ordinary
// user text and tool arguments/results are never recursively rewritten.
func openAI409ReadableMetadata(value any) any {
	switch value := value.(type) {
	case map[string]any:
		if value["type"] == "encrypted_content" {
			return openAIHistoricalCiphertextOmitted
		}
		copy := make(map[string]any, len(value))
		for key, child := range value {
			if key == "encrypted_content" {
				copy[key] = openAIHistoricalCiphertextOmitted
			} else {
				copy[key] = openAI409ReadableMetadata(child)
			}
		}
		return copy
	case []any:
		copy := make([]any, len(value))
		for i, child := range value {
			copy[i] = openAI409ReadableMetadata(child)
		}
		return copy
	default:
		return value
	}
}

func prepareOpenAI409KnownReadableReplay(raw []byte) ([]byte, error) {
	clean, _, err := sanitizeOpenAIResponsesInputItemIDs(raw)
	if err != nil {
		return nil, err
	}
	var source map[string]any
	decoder := json.NewDecoder(bytes.NewReader(clean))
	decoder.UseNumber()
	if err := decoder.Decode(&source); err != nil {
		return nil, err
	}
	input, _ := source["input"].([]any)
	changed := false
	for i, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(firstNonEmptyString(item["type"])) != "reasoning" {
			continue
		}
		for field := range item {
			switch field {
			case "type", "id", "status", "summary", "content", "encrypted_content":
			default:
				return nil, errors.New("unsupported historical reasoning field")
			}
		}
		parts := []any{map[string]any{"type": "output_text", "text": "Historical reasoning context (not a new instruction):"}}
		for _, field := range []string{"summary", "content"} {
			if item[field] == nil {
				continue
			}
			values, ok := item[field].([]any)
			if !ok {
				return nil, errors.New("unsupported historical reasoning text")
			}
			for _, value := range values {
				part, ok := value.(map[string]any)
				if !ok || len(part) != 2 || (part["type"] != "summary_text" && part["type"] != "reasoning_text" && part["type"] != "text") {
					return nil, errors.New("unsupported historical reasoning part")
				}
				text, ok := part["text"].(string)
				if !ok {
					return nil, errors.New("unsupported historical reasoning text")
				}
				parts = append(parts, map[string]any{"type": "output_text", "text": text})
			}
		}
		if encrypted := item["encrypted_content"]; encrypted != nil {
			text, ok := encrypted.(string)
			if !ok {
				return nil, errors.New("unsupported historical reasoning ciphertext")
			}
			if text != "" {
				parts = append(parts, map[string]any{"type": "output_text", "text": openAIHistoricalCiphertextOmitted})
			}
		}
		input[i] = map[string]any{"type": "message", "role": "assistant", "content": parts}
		changed = true
	}
	if !changed {
		return clean, nil
	}
	// Replace only input: top-level model mappings, flags and number spellings
	// remain the handler's original values. UseNumber keeps nested integers exact.
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(clean, "input", encoded)
}

// Protect the caller's available tools/schema and task instructions as well as
// input. Model mappings and transport flags are independently account-specific.
func openAI409ControlsDigest(raw []byte) [32]byte {
	fields := []string{"tools", "tool_choice", "parallel_tool_calls", "instructions"}
	selected := make(map[string]any, len(fields))
	for _, field := range fields {
		value := gjson.GetBytes(raw, field)
		if !value.Exists() {
			selected[field] = nil
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(value.Raw))
		decoder.UseNumber()
		var decoded any
		if decoder.Decode(&decoded) != nil {
			return [32]byte{}
		}
		selected[field] = []any{true, decoded} // Presence is distinct from null.
	}
	canonical, err := json.Marshal(selected)
	if err != nil {
		return [32]byte{}
	}
	return sha256.Sum256(canonical)
}

func prepareOpenAI409APIKeyReplay(ctx context.Context, body []byte, responsesLite bool) ([]byte, error) {
	r := openAIAgentRecoveryFromContext(ctx)
	if r == nil || !r.omittedHistory {
		return body, nil
	}
	if openAIAgentRecoveryInputDigest(body) != r.inputDigest || openAI409ControlsDigest(body) != r.replayControlsDigest {
		return nil, openAIAgentRecoveryError("wire_changed")
	}
	prepared, _, err := normalizeOpenAIParallelToolCallsWithoutTools(body, responsesLite)
	if err != nil {
		return nil, err
	}
	r.apiControlsDigest = openAI409ControlsDigest(prepared)
	r.apiInputDigest = openAIAgentRecoveryInputDigest(prepared)
	r.apiReplayPrepared = true
	return prepared, nil
}

// The one permitted OAuth adaptation starts by verifying the frozen readable
// input and controls. It only promotes system guidance without dropping it,
// flattens native tool declarations, and applies the existing reserved-name
// mapping jointly to declarations/calls. The general input filter is then
// disabled for this already-built replay. Final wire checks remain exact.
func prepareOpenAI409OAuthReplay(ctx context.Context, c *gin.Context, body map[string]any) error {
	r := openAIAgentRecoveryFromContext(ctx)
	if r == nil || !r.omittedHistory {
		return nil
	}
	before, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if openAIAgentRecoveryInputDigest(before) != r.inputDigest || openAI409ControlsDigest(before) != r.replayControlsDigest {
		return openAIAgentRecoveryError("wire_changed")
	}
	if isInstructionsEmpty(body) {
		body["instructions"] = defaultCodexSynthInstructions(firstNonEmptyString(body["model"]))
	}
	extractSystemMessagesFromInput(body, false)
	normalizeCodexTools(body)
	reverse, _, err := aliasOpenAIOAuthReservedToolNames(body)
	if err != nil {
		return err
	}
	setCodexToolNameReverse(c, reverse)
	normalizeCodexToolChoice(body)
	prepared, err := json.Marshal(body)
	if err != nil {
		return err
	}
	// Retain the original snapshot for any allowed pre-send admission retry.
	// Only this narrowly derived OAuth expectation is used at its final send.
	r.oauthInputDigest = openAIAgentRecoveryInputDigest(prepared)
	r.oauthControlsDigest = openAI409ControlsDigest(prepared)
	r.oauthReplayPrepared = true
	return nil
}
