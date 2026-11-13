package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Bounded, optional shared data plane. There is deliberately no local fallback.
// The principal budget spans all groups, models, sessions and task identities.
const (
	OpenAIReadableShadowMaxBytes   = 64 * 1024
	OpenAIReadableShadowMaxRecords = 8
	OpenAIReadableShadowTTL        = 30 * time.Minute
)

type OpenAIReadableShadowCache interface {
	GetOpenAIReadableShadow(ctx context.Context, principal, task string) (string, error)
	PutOpenAIReadableShadow(ctx context.Context, principal, task, value string, ttl time.Duration) error
	InvalidateOpenAIReadableShadow(ctx context.Context, principal, task string) error
}

type openAIReadableShadowContextKey struct{}

type openAIReadableShadowObservation struct {
	principal string
	task      string
	raw       string
	epoch     string
	fences    map[string]string
}

// This is observed readable context, not provider state or a generation to
// replay. Fences include the specific delivered output and all known sources.
type openAIReadableShadow struct {
	Version      int               `json:"version"`
	Scope        string            `json:"scope"`
	Input        json.RawMessage   `json:"input"`
	Instructions json.RawMessage   `json:"instructions,omitempty"`
	Fences       map[string]string `json:"fences"`
	Epoch        string            `json:"epoch"`
}

func (r *openAIAgentRecoveryRequest) shadowPrincipal() string {
	encoded, _ := json.Marshal([]any{1, r.userID, r.keyID})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (r *openAIAgentRecoveryRequest) shadowTaskKey(item [32]byte) string {
	encoded, _ := json.Marshal([]any{1, r.shadowPrincipal(), r.groupID, r.shadowModel, r.session, hex.EncodeToString(item[:])})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (r *openAIAgentRecoveryRequest) shadowEpochKey(item [32]byte) string {
	return r.shadowPrincipal() + ":" + r.shadowTaskKey(item)
}

// Restore before moderation, image/long-context admission and token reservation.
// The first supported future case is one exact opaque agent task with no new
// client intent or unpaired tool work. Existing readable requests never consult
// shadow. A miss/error leaves ordinary routing/refusal intact, never invents text.
func (s *OpenAIGatewayService) RestoreOpenAIReadableShadow(ctx context.Context, c *gin.Context, body []byte, session string) (context.Context, []byte) {
	if s == nil || s.settingService == nil || openAI409HasReadableIntent(body) ||
		isOpenAIResponsesCompactPath(c) || isOpenAINativeCompactionV2(c) ||
		!utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		return ctx, body
	}
	items := openAIAgentRecoveryItemDigests(body)
	if len(items) != 1 || !gjson.GetBytes(body, "input").IsArray() || len(gjson.GetBytes(body, "input").Array()) != 1 {
		return ctx, body
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() != "agent_message" {
			// A media/opaque user request or new tool result can be a distinct
			// task. Never replace that unknown intent with an older objective.
			return ctx, body
		}
	}
	keyID := getAPIKeyIDFromContext(c)
	r := &openAIAgentRecoveryRequest{keyID: keyID, userID: openAIReadableRecoveryUser(c, keyID),
		groupID: getOpenAIGroupIDFromContext(c), session: session, shadowModel: gjson.GetBytes(body, "model").String()}
	store, ok := s.cache.(OpenAIReadableShadowCache)
	if !ok || r.userID <= 0 || r.keyID <= 0 || r.session == "" || r.shadowModel == "" ||
		!s.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx) {
		return ctx, body
	}
	storeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	task := r.shadowTaskKey(items[0])
	epochs, ok := s.cache.(OpenAIAgentRecoveryEpochCache)
	if !ok {
		return ctx, body
	}
	var epoch string
	for {
		var err error
		epoch, err = epochs.GetOpenAIAgentRecoveryEpoch(storeCtx, r.shadowPrincipal()+":"+task)
		if err != nil {
			return ctx, body
		}
		if !strings.HasPrefix(epoch, "pending:") {
			break
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-storeCtx.Done():
			timer.Stop()
			return ctx, body
		case <-timer.C:
		}
	}
	if !strings.HasPrefix(epoch, "ready:pending:") {
		return ctx, body
	}
	raw, err := store.GetOpenAIReadableShadow(storeCtx, r.shadowPrincipal(), task)
	if err != nil || raw == "" || len(raw) > OpenAIReadableShadowMaxBytes {
		return ctx, body
	}
	var record openAIReadableShadow
	if json.Unmarshal([]byte(raw), &record) != nil || record.Version != 2 || record.Scope != task || epoch != "ready:"+record.Epoch ||
		len(record.Fences) == 0 || len(record.Fences) > 64 || !gjson.ParseBytes(record.Input).IsArray() ||
		s.checkOpenAIReadableShadowFences(storeCtx, record.Fences) != nil {
		return ctx, body
	}
	// Keep the supplied opaque item until the existing lossy builder detaches
	// it. Its original owner is still re-read at selection and the final send.
	var restored []json.RawMessage
	if json.Unmarshal(record.Input, &restored) != nil {
		return ctx, body
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		restored = append(restored, json.RawMessage(item.Raw))
	}
	input, err := json.Marshal(restored)
	if err != nil {
		return ctx, body
	}
	clean, err := sjson.SetRawBytes(body, "input", input)
	if err == nil && len(record.Instructions) > 0 {
		clean, err = sjson.SetRawBytes(clean, "instructions", record.Instructions)
	}
	if err != nil || !openAI409HasReadableIntent(clean) {
		return ctx, body
	}
	observation := &openAIReadableShadowObservation{principal: r.shadowPrincipal(), task: task, raw: raw, epoch: epoch, fences: record.Fences}
	if s.checkOpenAIReadableShadowObservation(storeCtx, observation) != nil {
		return ctx, body
	}
	return context.WithValue(WithRecoveredContextPrivacy(ctx), openAIReadableShadowContextKey{}, observation), clean
}

func (s *OpenAIGatewayService) checkOpenAIReadableShadowObservation(ctx context.Context, observation *openAIReadableShadowObservation) error {
	if observation == nil {
		return nil
	}
	store, ok := s.cache.(OpenAIReadableShadowCache)
	epochs, hasEpoch := s.cache.(OpenAIAgentRecoveryEpochCache)
	if !ok || !hasEpoch {
		return openAIAgentRecoveryError("store_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	raw, err := store.GetOpenAIReadableShadow(ctx, observation.principal, observation.task)
	if err != nil {
		return openAIAgentRecoveryError("store_unavailable")
	}
	epoch, err := epochs.GetOpenAIAgentRecoveryEpoch(ctx, observation.principal+":"+observation.task)
	if err != nil {
		return openAIAgentRecoveryError("store_unavailable")
	}
	if raw != observation.raw || epoch != observation.epoch {
		return openAIAgentRecoveryError("owner_conflict")
	}
	return s.checkOpenAIReadableShadowFences(ctx, observation.fences)
}

func (s *OpenAIGatewayService) checkOpenAIReadableShadowFences(ctx context.Context, fences map[string]string) error {
	if len(fences) == 0 {
		return nil
	}
	store, ok := s.cache.(OpenAIAgentRecoveryCache)
	if !ok {
		return openAIAgentRecoveryError("store_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for key, expected := range fences {
		binding, found, err := decodeOpenAIAgentRecoveryBinding(expected)
		if err != nil || !found || binding.pending != "" || binding.failed || binding.conflicted {
			return openAIAgentRecoveryError("owner_conflict")
		}
		actual, err := store.GetOpenAIAgentRecovery(ctx, key)
		if err != nil {
			return openAIAgentRecoveryError("store_unavailable")
		}
		if actual != expected {
			return openAIAgentRecoveryError("owner_conflict")
		}
	}
	return nil
}

// Only portable text and already paired tool calls/results are retained. An
// unknown item or an unpaired call/result loses shadow availability; it does
// not reject an otherwise valid original request or resubmit a tool.
func openAIReadableShadowCandidate(body []byte) *openAIReadableShadow {
	if !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) || !openAI409HasReadableIntent(body) {
		return nil
	}
	clean := body
	if input := gjson.GetBytes(clean, "input"); input.Type == gjson.String {
		value, _ := json.Marshal([]any{map[string]any{"type": "message", "role": "user", "content": input.String()}})
		clean, _ = sjson.SetRawBytes(clean, "input", value)
	}
	if omitted, changed := omitOpenAIHistoricalCiphertext(clean); changed {
		clean = omitted
	}
	var err error
	clean, err = prepareOpenAI409ReadableReplay(clean)
	if err != nil {
		return nil
	}
	input := gjson.GetBytes(clean, "input")
	if !input.IsArray() || len(input.Raw) > OpenAIReadableShadowMaxBytes {
		return nil
	}
	calls, outputs := make(map[string]string), make(map[string]string)
	for _, item := range input.Array() {
		kind, id := item.Get("type").String(), item.Get("call_id").String()
		switch kind {
		case "", "message":
			if !openAIReadableShadowTextOnly(item.Get("content")) {
				return nil
			}
			switch item.Get("role").String() {
			case "user", "assistant", "system", "developer":
			default:
				return nil
			}
		case "function_call", "custom_tool_call":
			if id == "" || calls[id] != "" || (item.Get("status").Exists() && item.Get("status").String() != "completed") {
				return nil
			}
			calls[id] = kind + "_output"
		case "function_call_output", "custom_tool_call_output":
			if id == "" || outputs[id] != "" || item.Get("output").Type == gjson.Null || !openAIReadableShadowTextOnly(item.Get("output")) ||
				(item.Get("status").Exists() && item.Get("status").String() != "completed") {
				return nil
			}
			outputs[id] = kind
		default:
			return nil
		}
	}
	if len(calls) != len(outputs) {
		return nil
	}
	for id, kind := range calls {
		if outputs[id] != kind {
			return nil
		}
	}
	record := &openAIReadableShadow{Version: 2, Input: json.RawMessage(input.Raw)}
	if instructions := gjson.GetBytes(clean, "instructions"); instructions.Exists() {
		if instructions.Type != gjson.String && instructions.Type != gjson.Null {
			return nil
		}
		if len(input.Raw)+len(instructions.Raw) > OpenAIReadableShadowMaxBytes {
			return nil
		}
		record.Instructions = json.RawMessage(instructions.Raw)
	}
	return record
}

func openAIReadableShadowTextOnly(content gjson.Result) bool {
	if content.Type == gjson.String || content.Type == gjson.Null {
		return true
	}
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		kind := part.Get("type").String()
		if (kind != "input_text" && kind != "output_text" && kind != "text" && kind != "summary_text") || part.Get("text").Type != gjson.String {
			return false
		}
	}
	return true
}

func openAIReadableShadowOutputSafe(body []byte, path string) bool {
	for _, item := range gjson.GetBytes(body, path).Array() {
		switch item.Get("type").String() {
		case "agent_message", "message", "reasoning":
		default:
			return false // In particular, new tool work is not a completed result.
		}
	}
	return true
}

func (s *OpenAIGatewayService) prepareOpenAIReadableShadow(ctx context.Context, r *openAIAgentRecoveryRequest, body []byte) {
	r.shadowCandidate = nil
	if r.userID <= 0 || r.shadowModel == "" || !r.shadowReadableIntent || r.disallowLearning ||
		s.settingService == nil || !s.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx) {
		return
	}
	if _, ok := s.cache.(OpenAIReadableShadowCache); ok {
		r.shadowCandidate = openAIReadableShadowCandidate(body)
	}
}

// Called inside delivery.once, after owner CAS, and only following a real
// successful downstream terminal write. Errors cannot undo that delivery or
// modify/revoke owners. No body, task, cache value or store error is logged.
func (d *openAIAgentRecoveryDelivery) publishReadableShadow(ctx context.Context) {
	r := d.request
	if len(r.outputs) == 0 || r.userID <= 0 || r.shadowModel == "" {
		return
	}
	store, ok := d.service.cache.(OpenAIReadableShadowCache)
	owners, owns := d.service.cache.(OpenAIAgentRecoveryCache)
	if !ok || !owns || d.service.settingService == nil || !d.service.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx) {
		return
	}
	for _, item := range r.outputs {
		if r.known[r.itemKey(item)] || r.shadowCandidate == nil || !r.shadowOutputSafe {
			// Reusing an opaque identity after more work cannot leave its old
			// task snapshot authoritative. Invalidate only an existing shadow;
			// never manufacture context for unsafe/unreadable output.
			_ = store.InvalidateOpenAIReadableShadow(ctx, r.shadowPrincipal(), r.shadowTaskKey(item))
		}
	}
	if r.shadowCandidate == nil || !r.shadowOutputSafe {
		return
	}
	fences := make(map[string]string)
	for key, route := range r.shadowFences {
		fences[key] = route
	}
	for source := r; source != nil; source = source.omittedSource {
		for _, key := range source.keys {
			if !source.known[key] {
				continue
			}
			encoded := hex.EncodeToString(key[:])
			raw, err := owners.GetOpenAIAgentRecovery(ctx, encoded)
			if err != nil || raw == "" {
				return
			}
			if expected, exists := fences[encoded]; exists && expected != raw {
				return
			}
			fences[encoded] = raw
		}
	}
	if len(fences) >= 64 || d.service.checkOpenAIReadableShadowFences(ctx, fences) != nil {
		return
	}
	route := encodeOpenAIAgentRecoveryBinding(d.binding)
	ttl := min(OpenAIReadableShadowTTL, d.service.openAIWSSessionStickyTTL())
	for _, item := range r.outputs {
		key := r.itemKey(item)
		// Echoed inputs are not new output provenance.
		if !d.keys[key] || r.known[key] {
			continue
		}
		encoded := hex.EncodeToString(key[:])
		actual, err := owners.GetOpenAIAgentRecovery(ctx, encoded)
		if err != nil || actual != route {
			continue
		}
		record := *r.shadowCandidate
		record.Scope = r.shadowTaskKey(item)
		if !d.epochs[r.shadowEpochKey(item)] {
			continue
		}
		record.Epoch = d.pending
		record.Fences = make(map[string]string, len(fences)+1)
		for key, value := range fences {
			record.Fences[key] = value
		}
		record.Fences[encoded] = route
		value, err := json.Marshal(record)
		if err != nil || len(value) > OpenAIReadableShadowMaxBytes || strings.TrimSpace(string(record.Input)) == "" {
			continue
		}
		_ = store.PutOpenAIReadableShadow(ctx, r.shadowPrincipal(), record.Scope, string(value), ttl)
	}
}
