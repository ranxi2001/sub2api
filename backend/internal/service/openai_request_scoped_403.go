package service

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// isOpenAIRequestScoped403 recognizes explicit request/transport failures from
// OpenAI and compatible relays. These are not evidence that the local credential is
// invalid, even when the relay reports HTTP 403. Keep this narrow so genuine
// account and workspace denials retain their existing handling.
func isOpenAIRequestScoped403(account *Account, statusCode int, body []byte) bool {
	if account == nil || account.Platform != PlatformOpenAI || statusCode != http.StatusForbidden {
		return false
	}
	var payload struct {
		Error *struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Code    string          `json:"code"`
		Type    string          `json:"type"`
		Detail  json.RawMessage `json:"detail"`
		Message json.RawMessage `json:"message"`
	}
	// Some relays append response.failed SSE after their JSON error. Decode the
	// first complete object, trusting only its error fields, never echoed input
	// or a later event in the body.
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&payload); err != nil {
		return false
	}
	// A denied Cyber program/model combination says nothing about ordinary
	// requests to that model. Do not poison either the account or model cooldown
	// with this request-specific entitlement error. Only trust the active error
	// envelope, never echoed input or a detail accompanying a different error.
	if payload.Error == nil {
		if payload.Code != "" || payload.Type != "" {
			return false
		}
		raw := payload.Detail
		if len(raw) == 0 || string(raw) == "null" {
			raw = payload.Message
		}
		var message string
		if err := json.Unmarshal(raw, &message); err != nil {
			return false
		}
		return isOpenAICyberAccessModelDeniedMessage(message)
	}
	code := strings.ToLower(strings.TrimSpace(payload.Error.Code))
	errorType := strings.ToLower(strings.TrimSpace(payload.Error.Type))
	if (code == "" || code == "permission_denied") &&
		(errorType == "" || errorType == "invalid_request_error" || errorType == "permission_error") &&
		isOpenAICyberAccessModelDeniedMessage(payload.Error.Message) {
		return true
	}
	if code == "content_policy_violation" || (code == "" && errorType == "content_policy_violation") {
		return true
	}
	return errorType == "upstream_error" && (code == "" || code == "upstream_error") &&
		strings.EqualFold(strings.TrimSpace(payload.Error.Message),
			"failed to WebSocket dial: expected handshake response status code 101 but got 403")
}

func isOpenAICyberAccessModelDeniedMessage(message string) bool {
	return strings.EqualFold(strings.TrimSpace(message),
		"The requested Cyber access program is not authorized for this model.")
}
