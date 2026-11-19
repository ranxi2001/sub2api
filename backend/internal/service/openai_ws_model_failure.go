package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

type openAIWSModelRejectionError struct{ payload []byte }

func (e *openAIWSModelRejectionError) Error() string { return ErrOpenAIWSModelUnavailable.Error() }
func (e *openAIWSModelRejectionError) Unwrap() error { return ErrOpenAIWSModelUnavailable }

// A successful WS handshake says nothing about a subsequent model rejection.
// Preserve explicit event status, including the top-level status used by Codex.
func openAIWSModelFailureStatus(account *Account, payload []byte) int {
	event := gjson.GetBytes(payload, "type").String()
	if event != "error" && event != "response.failed" {
		return 0
	}
	status := 0
	for _, path := range []string{"response.error.status_code", "response.error.status", "error.status_code", "error.status", "status_code", "status"} {
		if value := gjson.GetBytes(payload, path); value.Type == gjson.Number {
			status = int(value.Int())
			break
		}
	}
	if status == 0 {
		prefix := "error"
		if event == "response.failed" && gjson.GetBytes(payload, "response.error").IsObject() {
			prefix = "response.error"
		}
		code := strings.TrimSpace(gjson.GetBytes(payload, prefix+".code").String())
		errType := strings.TrimSpace(gjson.GetBytes(payload, prefix+".type").String())
		status = openAIWSErrorHTTPStatusFromRaw(code, errType)
		if strings.EqualFold(code, "model_not_found") {
			status = http.StatusNotFound
			if isOpenAICodexPlanGatedModelError(http.StatusBadRequest, payload) {
				status = http.StatusBadRequest
			}
		}
	}
	if isUpstreamModelNotFoundError(status, payload) ||
		(isOpenAIOAuthAccount(account) && isOpenAICodexPlanGatedModelError(status, payload)) {
		return status
	}
	return 0
}

func (s *OpenAIGatewayService) handleOpenAIWSModelFailure(ctx context.Context, account *Account, model string, payload []byte) bool {
	status := openAIWSModelFailureStatus(account, payload)
	if status == 0 || strings.TrimSpace(model) == "" {
		return false
	}
	// Handshake headers are a successful connection's snapshot, not headers for
	// this failure. Reuse HTTP's model policy and administrator rules without them.
	return s.handleOpenAIAccountUpstreamError(ctx, account, status, nil, payload, model)
}

// A model rejection may move a fresh, self-contained text request to another
// account. Do not infer migratable history from a first turn or absent output:
// pointers, opaque items, assistant history and tool results need separate proof.
// This predicate never transforms the request or discards client history.
func openAIWSModelFailureCanReplay(ctx context.Context, payload []byte) bool {
	if !OpenAIWSAgentRecoveryCanRetry(ctx) || OpenAIAgentRecoveryNoReplay(ctx) ||
		codexWSAnchorFromContext(ctx) != nil ||
		!openAIEncryptedJSONUnambiguous(payload) {
		return false
	}
	root := gjson.ParseBytes(payload)
	if !root.IsObject() || openAIWSModelFailureHasCiphertext(root, 0) {
		return false
	}
	for _, key := range []string{"previous_response_id", "conversation", "conversation_id"} {
		if value := root.Get(key); value.Exists() && value.Type != gjson.Null && value.String() != "" {
			return false
		}
	}
	input := root.Get("input")
	if input.Type == gjson.String {
		return true
	}
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if !item.IsObject() {
			return false
		}
		if kind := item.Get("type").String(); kind != "" && kind != "message" {
			return false
		}
		switch item.Get("role").String() {
		case "user", "system", "developer":
		default:
			return false
		}
		content := item.Get("content")
		if content.Type == gjson.String {
			continue
		}
		if !content.IsArray() {
			return false
		}
		for _, part := range content.Array() {
			if !part.IsObject() || part.Get("text").Type != gjson.String {
				return false
			}
			if kind := part.Get("type").String(); kind != "input_text" && kind != "text" {
				return false
			}
		}
	}
	return true
}

// Keep this guard independent of the optional history reconstruction machinery.
// Reject nested ciphertext even when it is attached to an otherwise plain item.
func openAIWSModelFailureHasCiphertext(value gjson.Result, depth int) bool {
	if depth > 64 {
		return true
	}
	if !value.IsObject() && !value.IsArray() {
		return false
	}
	opaque := false
	value.ForEach(func(key, child gjson.Result) bool {
		opaque = key.String() == "encrypted_content" || openAIWSModelFailureHasCiphertext(child, depth+1)
		return !opaque
	})
	return opaque
}

func (s *OpenAIGatewayService) newOpenAIWSModelFailoverError(account *Account, payload []byte) *UpstreamFailoverError {
	_, _, message := parseOpenAIWSErrorEventFields(payload)
	failure := s.newOpenAIAccountFailoverError(account, openAIWSModelFailureStatus(account, payload), nil, payload, message, false, false)
	failure.Reason = OpenAIWSModelUnavailableReason
	return failure
}
