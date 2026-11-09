package service

import (
	"encoding/json"
	"strings"
)

// GPT-5.5 retirement is an existing request-admission policy.
// https://learn.chatgpt.com/docs/models#gpt-55-retirement
func isRetiredOpenAISubscriptionModel(account *Account, upstreamModel string) bool {
	if account == nil || !account.IsOpenAI() || (!account.IsOpenAIOAuthLike() && !account.IsShadow()) {
		return false
	}
	model := canonicalizeOpenAIModelAliasSpelling(upstreamModel)
	return model == "gpt-5.5" || strings.HasPrefix(model, "gpt-5.5-")
}

// Hide legacy subscription choices from discovery while allowing explicitly
// configured requests to use the account's actual upstream capabilities.
func isHiddenOpenAISubscriptionModel(account *Account, upstreamModel string) bool {
	if account == nil || !account.IsOpenAI() || (!account.IsOpenAIOAuthLike() && !account.IsShadow()) {
		return false
	}
	if isRetiredOpenAISubscriptionModel(account, upstreamModel) {
		return true
	}
	model := canonicalizeOpenAIModelAliasSpelling(upstreamModel)
	return model == "gpt-5.4" || model == "gpt-5.4-mini" || strings.HasPrefix(model, "gpt-5.4-mini-") || model == "gpt-5.4-2026-03-05" ||
		model == "gpt-image-1.5" || strings.HasPrefix(model, "gpt-image-1.5-")
}

// Only remove entries blocked by retirement. Unknown models and other platforms
// retain their existing listing policy. An explicitly mapped replacement is
// allowed, but a public alias targeting retired GPT-5.5 is not.
func openAIModelRetiredForAccounts(accounts []Account, modelID string) bool {
	retired := false
	for i := range accounts {
		account := &accounts[i]
		if !account.IsOpenAI() {
			continue
		}
		target := resolveOpenAIAccountUpstreamModelForRequest(account, modelID, false)
		if isHiddenOpenAISubscriptionModel(account, target) {
			retired = true
		} else if account.IsModelSupported(modelID) {
			return false
		}
	}
	return retired
}

func filterRetiredOpenAIModelIDs(accounts []Account, models []string) []string {
	filtered := make([]string, 0, len(models))
	for _, model := range models {
		if !openAIModelRetiredForAccounts(accounts, model) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// Entries must already use client-visible names, not raw upstream IDs. Resolve
// each public name exactly as routing does, after projection and before the
// final client ETag is calculated. Preserve unknown fields.
func filterRetiredOpenAIPublicModelsBody(body []byte, accounts []Account, codex bool) ([]byte, bool, error) {
	field, idField := "data", "id"
	if codex {
		field, idField = "models", "slug"
	}
	envelope, entries, err := modelCatalogEntries(body, field)
	if err != nil {
		return nil, false, err
	}
	filtered := make([]json.RawMessage, 0, len(entries))
	for _, raw := range entries {
		var entry map[string]json.RawMessage
		var id string
		if json.Unmarshal(raw, &entry) == nil {
			_ = json.Unmarshal(entry[idField], &id)
		}
		if !openAIModelRetiredForAccounts(accounts, id) {
			filtered = append(filtered, raw)
		}
	}
	if len(filtered) == len(entries) {
		return body, false, nil
	}
	envelope[field], err = json.Marshal(filtered)
	if err != nil {
		return nil, false, err
	}
	encoded, err := json.Marshal(envelope)
	return encoded, true, err
}

// Compact-only administrator mappings are evaluated against their actual
// target, without treating an old client-visible name as a retired target.
func supportsOpenAIRequestModel(account *Account, requestedModel string, requireCompact bool) bool {
	if account.Platform == PlatformOpenAI && account.HasOAuthDegradationAction() && !account.oauthDegradationAllows(resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact)) {
		return false
	}
	if account.Platform == PlatformOpenAI && !account.oauthInitialSelectionAllows(resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact)) {
		return false
	}
	if isRetiredOpenAISubscriptionModel(account, resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact)) {
		return false
	}
	return account.isModelMappingSupported(requestedModel)
}
