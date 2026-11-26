package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// An HTTP 401 alone is not evidence about the selected account: proxies and
// capacity relays also return it. Only structured credential evidence may open
// a new cross-account retry window on a stateful transport.
func isOpenAIExplicitAccountCredential401(status int, headers http.Header, body []byte) bool {
	if status != http.StatusUnauthorized || headers.Get("Proxy-Authenticate") != "" ||
		!openAIEncryptedJSONUnambiguous(body) {
		return false
	}
	fields := gjson.GetBytes(body, "error")
	if !fields.IsObject() {
		return false
	}
	code := strings.TrimSpace(fields.Get("code").String())
	// A type such as authentication_error is also emitted by relays and policy
	// gateways. Only an explicit, allowlisted account code is sufficient.
	switch strings.ToLower(code) {
	case "invalid_api_key", "api_key_disabled", "invalid_token", "access_token_invalid",
		"token_revoked", "token_invalidated", "invalid_credentials", "credential_invalid",
		"account_disabled", "account_deactivated", "account_suspended", "deactivated_workspace",
		"workspace_disabled", "workspace_suspended", "organization_disabled", "organization_suspended":
		return true
	default:
		return false
	}
}

// Only an unambiguous, non-conflicting quota envelope opens the pre-send
// account retry window. A Retry-After response is treated as short throttling;
// it must retain the existing rate-limit path.
func isOpenAIExplicitQuota429(status int, headers http.Header, body []byte) bool {
	if status != http.StatusTooManyRequests || headers.Get("Retry-After") != "" ||
		headers.Get("Proxy-Authenticate") != "" || !openAIEncryptedJSONUnambiguous(body) {
		return false
	}
	error := gjson.GetBytes(body, "error")
	if !error.IsObject() {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(error.Get("code").String()))
	typ := strings.ToLower(strings.TrimSpace(error.Get("type").String()))
	quotaCode := code == "usage_limit_reached" || code == "insufficient_quota" || code == "quota_exceeded"
	quotaType := typ == "usage_limit_reached" || typ == "quota_exceeded" || typ == "insufficient_quota"
	if code != "" && !quotaCode {
		return false
	}
	if typ != "" && !quotaType && !(quotaCode && typ == "rate_limit_error") {
		return false
	}
	return quotaCode || quotaType
}

// Only self-contained function declarations are portable across credentials.
// Hosted search, containers and vector stores carry account-owned state even
// when the current input is a plain string.
func openAIWSReplayToolsPortable(tools gjson.Result) bool {
	if !tools.Exists() || tools.Type == gjson.Null {
		return true
	}
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if !tool.IsObject() || tool.Get("type").String() != "function" ||
			strings.TrimSpace(tool.Get("name").String()) == "" {
			return false
		}
		for _, key := range []string{"vector_store_ids", "file_search", "container", "code_interpreter", "computer", "namespace"} {
			if tool.Get(key).Exists() {
				return false
			}
		}
		known := true
		tool.ForEach(func(key, _ gjson.Result) bool {
			switch key.String() {
			case "type", "name", "description", "parameters", "strict", "defer_loading":
			default:
				known = false
			}
			return known
		})
		if !known {
			return false
		}
		parameters := tool.Get("parameters")
		if parameters.Exists() && !parameters.IsObject() {
			return false
		}
	}
	return true
}

type openAIWSHandshakeReplayKey struct{}

// Capture this before any account projection or history normalization. Both
// the immutable client request and the exact outgoing payload must be portable.
func openAIWSHandshakePayloadPortable(ctx context.Context, raw []byte) bool {
	if !openAIWSModelFailureCanReplay(ctx, raw) {
		return false
	}
	root := gjson.ParseBytes(raw)
	if !openAIWSReplayToolsPortable(root.Get("tools")) {
		return false
	}
	choice := root.Get("tool_choice")
	if choice.Exists() && choice.Type != gjson.Null && choice.Type != gjson.String &&
		(!choice.IsObject() || choice.Get("type").String() != "function" || choice.Get("name").Type != gjson.String) {
		return false
	}
	for _, item := range root.Get("input").Array() {
		known := true
		item.ForEach(func(key, _ gjson.Result) bool {
			known = key.String() == "type" || key.String() == "role" || key.String() == "content"
			return known
		})
		if !known {
			return false
		}
		for _, part := range item.Get("content").Array() {
			part.ForEach(func(key, _ gjson.Result) bool {
				known = key.String() == "type" || key.String() == "text"
				return known
			})
			if !known {
				return false
			}
		}
	}
	allowed := map[string]bool{"type": true, "model": true, "input": true, "instructions": true,
		"stream": true, "store": true, "tools": true, "tool_choice": true, "parallel_tool_calls": true,
		"max_output_tokens": true, "temperature": true, "top_p": true, "reasoning": true,
		"text": true, "include": true, "truncation": true, "metadata": true,
		"prompt_cache_key": true, "prompt_cache_retention": true, "service_tier": true,
		"client_metadata": true, "safety_identifier": true}
	portable := true
	root.ForEach(func(key, value gjson.Result) bool {
		portable = allowed[key.String()] || ((key.String() == "previous_response_id" || key.String() == "conversation" || key.String() == "conversation_id") && (value.Type == gjson.Null || value.String() == ""))
		return portable
	})
	return portable
}

// This private wrapper is minted only after a pool handshake failed before
// prewarm/response.create. It cannot be inferred from missing output.
type openAIWSHandshakeAccountFailover struct {
	cause error
	meter *regionalMeterAttempt
}

func (e *openAIWSHandshakeAccountFailover) Error() string { return e.cause.Error() }
func (e *openAIWSHandshakeAccountFailover) Unwrap() error { return e.cause }
