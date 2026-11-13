package service

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func openAI400HistoryRecoveryApplied(ctx context.Context) bool {
	r := openAIAgentRecoveryFromContext(ctx)
	return r != nil && r.omitted400History
}

func (s *SettingService) IsOpenAI400OmitEncryptedHistoryEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil || ctx.Err() != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAI400OmitEncryptedHistoryEnabled)
	return err == nil && strings.TrimSpace(value) == "true"
}

// Called only for an explicit native HTTP 400 encryption rejection, after the
// existing precise recovery declined or used its budget. Never an SSE terminal,
// transport error, arbitrary 400, account failover or initial selection retry.
func (s *OpenAIGatewayService) prepareOpenAI400HistoryRecovery(ctx context.Context, c *gin.Context, account *Account, body []byte, attempts int) (context.Context, []byte, bool) {
	r := openAIAgentRecoveryFromContext(ctx)
	d := &OpenAI400HistoryDiagnostic{}
	if r != nil {
		d.After409, d.Applied = r.omittedHistory, r.omitted400History
	}
	refuse := func(reason string) (context.Context, []byte, bool) {
		d.Decision = reason
		if c != nil {
			c.Set(openAI400HistoryDiagnosticKey, d)
		}
		return ctx, body, false
	}
	if isControlledExperiment(ctx) || ctx.Err() != nil || c == nil || c.Request == nil ||
		(c.Request != nil && c.Request.Context().Err() != nil) || openAIStreamClientOutputStarted(c, false) {
		return refuse("canceled_or_output_started")
	}
	if d.Applied || attempts >= 3 {
		return refuse("retry_exhausted")
	}
	if s == nil || !s.settingService.IsOpenAI400OmitEncryptedHistoryEnabled(ctx) {
		return refuse("disabled")
	}
	if r == nil || r.keyID <= 0 || r.session == "" || r.model == "" || r.disallowLearning ||
		r.nativeCompaction || r.previousResponseID != "" || GetOpenAIClientTransport(c) == OpenAIClientTransportWS ||
		s.checkOpenAIAgentRecoveryDispatch(ctx, c, account, body) != nil {
		return refuse("unsupported_context")
	}
	if r.lookupErr != nil || r.conflict || (r.owner != nil && (r.owner.pending != "" || r.owner.failed || r.owner.conflicted)) {
		return refuse("ownership_unavailable")
	}
	// Native send admission must have completed; no output may have been exposed
	// or reserved. Keep the exact admitted route, including credential identity.
	if r.delivery == nil || len(r.delivery.keys) != 0 || len(r.outputs) != 0 {
		return refuse("delivery_unconfirmed")
	}
	if r.preservesHistory() && openAIAgentRecoveryInputDigest(body) != r.inputDigest {
		return refuse("history_integrity")
	}
	if s.checkOpenAIAgentRecoveryRecords(ctx, r, r.delivery.binding) != nil {
		return refuse("ownership_unavailable")
	}
	clean, changed := omitOpenAIHistoricalCiphertextForRecovery(body, true)
	if !changed {
		return refuse("no_eligible_history")
	}
	if ctx.Err() != nil || (c.Request != nil && c.Request.Context().Err() != nil) {
		return refuse("canceled_or_output_started")
	}
	r.delivery.finish(false)
	next := *r
	next.omitted400History = true
	next.delivery, next.outputs = nil, nil
	route := r.delivery.binding
	next.retry400Route = &route
	// Freeze ONLY this explicitly authorized transformation. The source snapshot
	// remains unchanged. Original keys/known/owner are retained for record and
	// route checks; omitted inputs must never be published as consumed inputs.
	next.inputDigest = openAIAgentRecoveryInputDigest(clean)
	next.items = openAIAgentRecoveryItemDigests(clean)
	d.Applied, d.Decision = true, "same_account_retry_scheduled"
	c.Set(openAI400HistoryDiagnosticKey, d)
	return context.WithValue(ctx, openAIAgentRecoveryContextKey{}, &next), clean, true
}

func openAI400CurrentTaskReadable(item map[string]any) bool {
	if text, ok := item["content"].(string); ok {
		return strings.TrimSpace(text) != ""
	}
	parts, _ := item["content"].([]any)
	for _, raw := range parts {
		part, _ := raw.(map[string]any)
		text, _ := part["text"].(string)
		if part["type"] == "input_text" && strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

// Reasoning ciphertext cannot be reconstituted. Preserve all supported visible
// text in ordinary history, with an explicit loss marker; unknown shapes stop
// recovery rather than silently dropping readable fields or attachments.
func omitOpenAI400Reasoning(item map[string]any) (map[string]any, bool) {
	for key := range item {
		switch key {
		case "type", "id", "status", "encrypted_content", "summary", "content":
		default:
			return nil, false
		}
	}
	parts := []any{map[string]any{"type": "output_text", "text": openAIHistoricalCiphertextOmitted}}
	for _, field := range []string{"summary", "content"} {
		value := item[field]
		if value == nil {
			continue
		}
		if text, ok := value.(string); ok && field == "content" {
			parts = append(parts, map[string]any{"type": "output_text", "text": text})
			continue
		}
		items, ok := value.([]any)
		if !ok {
			return nil, false
		}
		for _, raw := range items {
			part, ok := raw.(map[string]any)
			if !ok || len(part) != 2 {
				return nil, false
			}
			kind, _ := part["type"].(string)
			text, ok := part["text"].(string)
			if !ok || (kind != "summary_text" && kind != "reasoning_text" && kind != "text") {
				return nil, false
			}
			parts = append(parts, map[string]any{"type": "output_text", "text": text})
		}
	}
	return map[string]any{"type": "message", "role": "assistant", "content": parts}, true
}
