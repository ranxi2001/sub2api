package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
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
	if s.openAILegacyCompatibility() {
		return ctx, body, false, nil
	}
	var refusal *OpenAIAgentRecoveryError
	if !errors.As(cause, &refusal) {
		return ctx, body, false, nil
	}
	r := openAIAgentRecoveryFromContext(ctx)
	allowed := refusal.Reason == "owner_unavailable" || refusal.Reason == "route_changed" || refusal.Reason == "wire_changed" ||
		refusal.Reason == "owner_rate_limited" || refusal.Reason == "owner_overloaded" ||
		refusal.Reason == "http_previous_response_unsupported" || (refusal.Reason == "owner_conflict" && r != nil && r.mixedOwners)
	// A known owner's cooldown is another pre-send availability failure. Under
	// the explicit lossy policy, rebuild readable context for normal selection
	// without clearing that cooldown or retrying a dispatched generation.
	if !allowed || s == nil || r == nil || r.disallowLearning || r.omittedHistory || r.delivery != nil || s.settingService == nil ||
		!s.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx) {
		return ctx, body, false, nil
	}
	if r.lookupErr != nil {
		return ctx, body, false, r.lookupErr
	}
	if r.conflict {
		return ctx, body, false, openAIRecoveryDiagnosticError("owner_conflict", r.diagnostics.subreason, r)
	}
	if r.owner != nil && (r.owner.pending != "" || r.owner.failed || r.owner.conflicted) {
		return ctx, body, false, openAIRecoveryDiagnosticError("owner_conflict", openAIRecoveryOwnerDetail(*r.owner, true), r)
	}
	_, websocket := ctx.Value(openAIWSAgentRecoveryRetryKey{}).(*atomic.Bool)
	mixedWS := websocket && r.mixedOwners
	if mixedWS && !r.hasCompletedMixedOwners() {
		return ctx, body, false, openAIRecoveryDiagnosticError("owner_conflict", "owner_delivery_unknown", r)
	}
	if mixedWS {
		for _, item := range gjson.GetBytes(body, "input").Array() {
			kind := item.Get("type").String()
			if (kind == "function_call_output" || kind == "custom_tool_call_output") && !openAIReadableShadowTextOnly(item.Get("output")) {
				return ctx, body, false, openAIRecoveryDiagnosticError("readable_history_unsupported", "tool_history_unsupported", r)
			}
		}
	}
	if len(r.items) == 0 && !openAI409HasProviderState(body) {
		return ctx, body, false, nil
	}
	if !openAI409HasReadableIntent(body) {
		return ctx, body, false, openAIRecoveryDiagnosticError("readable_intent_missing", "readable_intent_missing", r)
	}
	if websocket {
		if err := s.verifyOpenAIToolInputs(ctx, r, body); err != nil {
			return ctx, body, false, err
		}
	} else {
		// HTTP discovery recovery needs the same actually delivered call and
		// catalog receipt. Ordinary function/custom HTTP behavior is unchanged.
		if err := s.verifyOpenAIToolInputProofs(ctx, r, body, true); err != nil {
			return ctx, body, false, err
		}
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
	if mixedWS {
		if _, detail := inspectOpenAIWSCompletedTools(body); detail != "" {
			return ctx, body, false, openAIRecoveryDiagnosticError("owner_conflict", detail, r)
		}
		clean, err = projectOpenAIWSCompletedTools(clean)
		if err != nil {
			return ctx, body, false, withOpenAIRecoveryDiagnostics(err, r, "tool_history_unsupported")
		}
		r.requireCompletedOwners = true
	}
	fresh := context.WithValue(ctx, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{})
	next := s.WithOpenAIAgentMessageRecovery(WithOpenAIEncryptedReasoningAffinity(fresh, clean), c, clean, session, model)
	request := openAIAgentRecoveryFromContext(next)
	// Preserve the sanitized input at the actual send and reuse the existing
	// native HTTP delivery fence and no-replay gates. Old owner records stay put.
	request.omittedHistory = true
	request.inputDigest = openAIAgentRecoveryInputDigest(clean)
	request.replayControlsDigest = openAI409ControlsDigest(clean)
	request.controlFingerprints = openAI409FingerprintControls(clean)
	request.omittedSource = r
	request.toolProofs = r.toolProofs
	return next, clean, true, nil
}

// Provider state and mixed completed producers cannot be forwarded as one new
// account's conversation. Rebuild before selection, after all item fences were
// read. A healthy single producer without pointers retains ordinary routing.
func (s *OpenAIGatewayService) PrepareOpenAI409ReadableContinuation(ctx context.Context, c *gin.Context, body []byte, session, model string) (nextCtx context.Context, nextBody []byte, changed bool, resultErr error) {
	if s.openAILegacyCompatibility() {
		return ctx, body, false, nil
	}
	if next, clean, changed := s.PrepareOpenAILossyHistoryRestart(ctx, c, body, session, model); changed {
		return next, clean, true, nil
	}
	defer func() {
		if _, ws := ctx.Value(openAIWSAgentRecoveryRetryKey{}).(*atomic.Bool); ws && resultErr != nil {
			// Some protocol counterexamples restore the pre-policy WS file.
			// Keep that historical implementation buildable without injecting
			// the candidate recovery algorithm into its RED baseline.
			if restarter, available := any(s).(interface {
				restartOpenAIWSClientReadableHistory(context.Context, *gin.Context, []byte, string, string, error) (context.Context, []byte, bool)
			}); available {
				if next, clean, ok := restarter.restartOpenAIWSClientReadableHistory(ctx, c, body, session, model, resultErr); ok {
					nextCtx, nextBody, changed, resultErr = next, clean, true, nil
				}
			}
		}
	}()

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
	if s.openAILegacyCompatibility() {
		return false
	}
	if s != nil && s.settingService != nil {
		policy, ok := any(s.cfg).(interface{ OpenAILossyHistoryRestartEnabled() bool })
		if ok && policy.OpenAILossyHistoryRestartEnabled() && utf8.Valid(body) && openAIEncryptedJSONUnambiguous(body) &&
			s.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx) {
			// This grants only permission to discard the pointer. The handler
			// requires a successful rebuild and verifies its removal from both
			// body copies before dispatch; no foreign response is consumed.
			_, changed := discardOpenAIProviderHistory(body)
			if changed {
				return true
			}
		}
	}
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
	clean, err := normalizeOpenAIReplayMetadata(raw)
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

type openAI409ControlFingerprints struct {
	values   [4][32]byte
	observed [4]bool
}

var openAI409ControlFields = [...]string{"tools", "tool_choice", "parallel_tool_calls", "instructions"}

func openAI409FingerprintControls(raw []byte) openAI409ControlFingerprints {
	var fingerprints openAI409ControlFingerprints
	for i, field := range openAI409ControlFields {
		value := gjson.GetBytes(raw, field)
		var encoded []byte
		if !value.Exists() {
			encoded, _ = json.Marshal([]any{false})
		} else {
			decoder := json.NewDecoder(strings.NewReader(value.Raw))
			decoder.UseNumber()
			var decoded any
			if decoder.Decode(&decoded) != nil {
				continue
			}
			encoded, _ = json.Marshal([]any{true, decoded})
		}
		if encoded == nil {
			continue
		}
		fingerprints.values[i] = sha256.Sum256(encoded)
		fingerprints.observed[i] = true
	}
	return fingerprints
}

func openAI409ControlDiagnostic(raw []byte, expected openAI409ControlFingerprints, source string, digestChanged, previousResponsePresent, conversationPresent bool) OpsRecoveryControls {
	actual := openAI409FingerprintControls(raw)
	checks := [4]string{}
	complete := true
	for i := range openAI409ControlFields {
		switch {
		case !expected.observed[i] || !actual.observed[i]:
			checks[i] = "unknown"
			complete = false
		case expected.values[i] != actual.values[i]:
			checks[i] = "changed"
		default:
			checks[i] = "match"
		}
	}
	observation := "complete"
	if !complete {
		observation = "partial"
	}
	return OpsRecoveryControls{
		Observation: observation, Snapshot: source,
		Tools: checks[0], ToolChoice: checks[1], ParallelToolCalls: checks[2], Instructions: checks[3],
		DigestChanged: digestChanged, PreviousResponseIDPresent: previousResponsePresent, ConversationPresent: conversationPresent,
	}
}

func prepareOpenAI409APIKeyReplay(ctx context.Context, body []byte, responsesLite bool) ([]byte, error) {
	r := openAIAgentRecoveryFromContext(ctx)
	if r == nil || !r.omittedHistory {
		return body, nil
	}
	if err := checkOpenAIRecoveryProjectionSource(ctx, "apikey_projection_source", body, r); err != nil {
		return nil, err
	}
	prepared, _, err := normalizeOpenAIParallelToolCallsWithoutTools(body, responsesLite)
	if err != nil {
		return nil, err
	}
	r.apiControlsDigest = openAI409ControlsDigest(prepared)
	r.apiFingerprints = openAI409FingerprintControls(prepared)
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
	if err := checkOpenAIRecoveryProjectionSource(ctx, "oauth_projection_source", before, r); err != nil {
		return err
	}
	// ChatGPT rejects this output-only metadata. Normalize before freezing the
	// OAuth representation; the later ordinary transform must be idempotent.
	normalizeOpenAIOAuthResponsesCompatibilityFields(body)
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
	r.oauthFingerprints = openAI409FingerprintControls(prepared)
	r.oauthReplayPrepared = true
	return nil
}

// A rebuilt request is a new native Responses conversation. An API-key
// account's ordinary passthrough preference must not route this prepared
// request around the native input and delivery checks. Subsequent single-owner
// turns must keep the learned native route too; exact route/credentials/wire
// validation still runs before sending.
func openAI409UsesNativeAPIKey(ctx context.Context, account *Account) bool {
	r := openAIAgentRecoveryFromContext(ctx)
	return r != nil && account != nil && account.IsOpenAIApiKey() &&
		(r.omittedHistory || (r.ownerBindingRequired() && r.owner.accountID == account.ID))
}

func openAIRecoveryWireRefusal(ctx context.Context, stage string, details ...zap.Field) error {
	fields := append([]zap.Field{zap.String("stage", stage)}, details...)
	logger.FromContext(ctx).Warn("openai.encrypted_history_409_refused", fields...)
	return openAIRecoveryDiagnosticError("wire_changed", stage, openAIAgentRecoveryFromContext(ctx))
}

func openAIRecoveryControlsWireRefusal(ctx context.Context, report OpsRecoveryControls) error {
	logger.FromContext(ctx).Warn("openai.encrypted_history_409_refused",
		zap.String("stage", "controls_integrity"),
		zap.String("controls_observation", report.Observation), zap.String("controls_snapshot", report.Snapshot),
		zap.String("tools", report.Tools), zap.String("tool_choice", report.ToolChoice),
		zap.String("parallel_tool_calls", report.ParallelToolCalls), zap.String("instructions", report.Instructions),
		zap.Bool("controls_digest_changed", report.DigestChanged),
		zap.Bool("previous_response_id_present", report.PreviousResponseIDPresent),
		zap.Bool("conversation_present", report.ConversationPresent))
	err := openAIRecoveryDiagnosticError("wire_changed", "controls_integrity", openAIAgentRecoveryFromContext(ctx))
	if recovery, ok := err.(*OpenAIAgentRecoveryError); ok {
		diagnostic := *recovery.diagnostic
		diagnostic.controls = &report
		recovery.diagnostic = &diagnostic
	}
	return err
}

func checkOpenAIRecoveryProjectionSource(ctx context.Context, stage string, body []byte, r *openAIAgentRecoveryRequest) error {
	inputChanged := openAIAgentRecoveryInputDigest(body) != r.inputDigest
	controlsChanged := openAI409ControlsDigest(body) != r.replayControlsDigest
	if !inputChanged && !controlsChanged {
		return nil
	}
	// Fixed metadata only. Do not log the input, tools, instructions or digests.
	return openAIRecoveryWireRefusal(ctx, stage,
		zap.Bool("input_changed", inputChanged), zap.Bool("controls_changed", controlsChanged))
}

// Derive the HTTP representation only from the verified readable snapshot,
// before Forward's compatibility pipeline touches it. Keep the original
// context immutable so a later account admission starts from the same input.
// This is not a digest update at the final send boundary.
func (s *OpenAIGatewayService) prepareOpenAI409HTTPProjection(ctx context.Context, c *gin.Context, account *Account, body []byte) (context.Context, []byte, error) {
	r := openAIAgentRecoveryFromContext(ctx)
	if r == nil || !r.omittedHistory {
		return ctx, body, nil
	}
	if err := checkOpenAIRecoveryProjectionSource(ctx, "http_projection_source", body, r); err != nil {
		return ctx, body, err
	}
	clean := s.normalizeRequestTimezone(ctx, account, body, "http")
	var err error
	if account.IsOpenAIOAuthLike() {
		clean, _, err = stripOpenAICodexUnsupportedWebSearchFields(clean)
		if err != nil {
			return ctx, body, err
		}
	}
	clean, _, err = normalizeOpenAIResponsesLegacyIngressWithConfig(s.cfg, clean)
	if err != nil {
		return ctx, body, err
	}
	clean, _, err = sanitizeOpenAIResponsesToolSchemasForPlatform(clean, account.Platform)
	if err != nil {
		return ctx, body, err
	}
	if isOpenAIResponsesLiteHeader(c.GetHeader(responsesLiteHeader)) {
		clean, _, err = normalizeOpenAIResponsesLitePayloadForAccount(clean, account)
		if err != nil {
			return ctx, body, err
		}
	}
	clean, err = normalizeOpenAIResponsesNamespaces(c, account, clean, OpenAIUpstreamTransportHTTPSSE, false, false)
	if err != nil {
		return ctx, body, err
	}
	// Match the existing native builder's empty-image sanitation before
	// freezing this projected representation. Keep readable text, completed
	// tool results, order and exact JSON numbers; do not rebase at final send.
	if openAIRequestBodyMayContainEmptyBase64InputImage(clean) {
		var decoded map[string]any
		if err := decodeOpenAIJSONUseNumber(clean, &decoded); err != nil {
			return ctx, body, err
		}
		if sanitizeEmptyBase64InputImagesInOpenAIRequestBodyMap(decoded) {
			clean, err = json.Marshal(decoded)
			if err != nil {
				return ctx, body, err
			}
		}
	}
	clean, err = s.projectOpenAI409ImageControls(ctx, c, account, clean)
	if err != nil {
		return ctx, body, err
	}
	snapshot := *r
	snapshot.inputDigest = openAIAgentRecoveryInputDigest(clean)
	snapshot.replayControlsDigest = openAI409ControlsDigest(clean)
	snapshot.controlFingerprints = openAI409FingerprintControls(clean)
	snapshot.apiReplayPrepared, snapshot.oauthReplayPrepared = false, false
	return context.WithValue(ctx, openAIAgentRecoveryContextKey{}, &snapshot), clean, nil
}

// Account-scoped routing exclusions are applied before acquiring a candidate.
// The same boundary remains enforced by the dispatch guard after revalidation.
func (s *OpenAIGatewayService) openAI409AccountRouteSupported(ctx context.Context, account *Account, model string) bool {
	r := openAIAgentRecoveryFromContext(ctx)
	if r == nil || !r.omittedHistory {
		return true
	}
	return account != nil && account.IsOpenAI() && !account.HasManagedOpenAIConnections() && account.AuthConnectionID == "" &&
		!account.IsCopilotSDKEnabled() && !account.IsSIWCEnabled() &&
		!account.IsExcelBPSEnabledForModel(model) && !account.IsPrismBrowserEnabledForModel(model) &&
		s.resolveUpstreamProtocolFor(ctx, account, APIProtocolResponses, account.GetMappedModel(model)) == APIProtocolResponses &&
		(!account.IsOpenAIPassthroughEnabled() || account.IsOpenAIApiKey())
}
