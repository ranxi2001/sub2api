package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

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

// TryOmitOpenAI409History is called only after initial account selection fails,
// before any upstream attempt. It never handles provider responses or retries.
func (s *OpenAIGatewayService) TryOmitOpenAI409History(ctx context.Context, c *gin.Context, body []byte, session, model string, cause error) (context.Context, []byte, bool) {
	var refusal *OpenAIAgentRecoveryError
	if !errors.As(cause, &refusal) || (refusal.Reason != "owner_unavailable" && refusal.Reason != "route_changed" && refusal.Reason != "wire_changed") {
		return ctx, body, false
	}
	r := openAIAgentRecoveryFromContext(ctx)
	if s == nil || r == nil || r.owner == nil || r.conflict || r.lookupErr != nil || r.disallowLearning || r.omittedHistory ||
		r.owner.pending != "" || r.owner.failed || r.owner.conflicted ||
		!s.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx) {
		return ctx, body, false
	}
	clean, changed := omitOpenAIHistoricalCiphertext(body)
	if !changed || ctx.Err() != nil {
		return ctx, body, false
	}
	// Freeze the same narrowly normalized replay metadata that native Responses
	// will send. Do this before recording the digest, never after arbitrary
	// forwarder transformations. Readable content and call relationships stay
	// covered by the original exact-input gate.
	clean, err := normalizeOpenAI409ReplayMetadata(clean)
	if err != nil {
		return ctx, body, false
	}
	next := s.WithOpenAIAgentMessageRecovery(ctx, c, clean, session, model)
	request := openAIAgentRecoveryFromContext(next)
	// Preserve the sanitized input at the actual send and reuse the existing
	// native HTTP delivery fence and no-replay gates. Old owner records stay put.
	request.omittedHistory = true
	request.inputDigest = openAIAgentRecoveryInputDigest(clean)
	return next, clean, true
}

// Omission is limited to history before the latest user/agent task. Tool
// arguments, readable results, media, reasoning and numeric precision survive.
// A ciphertext in the current task causes the entire operation to be refused.
func omitOpenAIHistoricalCiphertext(raw []byte) ([]byte, bool) {
	return omitOpenAIHistoricalCiphertextForRecovery(raw, false)
}

// normalizeOpenAI409ReplayMetadata only removes output-only replay metadata.
// It must not use the general reasoning normalizers: those may delete entire
// reasoning items or visible content. Such changes still fail the send gate.
func normalizeOpenAI409ReplayMetadata(raw []byte) ([]byte, error) {
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
	storeFalse := gjson.GetBytes(clean, "store").Type == gjson.False
	changed := false
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(firstNonEmptyString(item["type"])) != "reasoning" {
			continue
		}
		if _, exists := item["status"]; exists {
			delete(item, "status")
			changed = true
		}
		if !storeFalse {
			continue
		}
		encrypted, _ := item["encrypted_content"].(string)
		if strings.TrimSpace(encrypted) == "" {
			// Preserve readable reasoning even if the general store=false replay
			// path cannot transport it. Never bless its removal as normalization.
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(firstNonEmptyString(item["id"])), "rs_") {
			delete(item, "id")
			changed = true
		}
		if summary, exists := item["summary"]; !exists || summary == nil {
			item["summary"] = []any{}
			changed = true
		}
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
