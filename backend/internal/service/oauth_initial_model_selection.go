package service

import (
	"maps"
	"slices"
	"strings"
	"unicode"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OAuthInitialModelSelectionKey is an account-owned snapshot, never a live link
// to the global template. An empty array explicitly denies every model.
const OAuthInitialModelSelectionKey = "oauth_initial_selected_models"

func validateOAuthSelectedModels(models []string) error {
	if len(models) > 500 {
		return infraerrors.BadRequest("AUTO_CONFIG_INVALID", "at most 500 selected models allowed")
	}
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		if model == "" || len(model) > 256 || strings.Contains(model, "*") || strings.IndexFunc(model, unicode.IsSpace) >= 0 || strings.IndexFunc(model, unicode.IsControl) >= 0 || seen[model] {
			return infraerrors.BadRequest("AUTO_CONFIG_INVALID", "selected models must be unique exact names without whitespace, wildcards or control characters (1–256 bytes)")
		}
		seen[model] = true
	}
	return nil
}

func (a *Account) hasOAuthInitialModelSelection() bool {
	if a == nil || a.Type != AccountTypeOAuth {
		return false
	}
	value, exists := a.Credentials[OAuthInitialModelSelectionKey]
	return exists && value != nil
}

func (a *Account) oauthInitialSelectionAllows(model string) bool {
	if !a.hasOAuthInitialModelSelection() {
		return true
	}
	switch models := a.Credentials[OAuthInitialModelSelectionKey].(type) {
	case []string:
		return slices.Contains(models, model)
	case []any:
		for _, value := range models {
			if value == model {
				return true
			}
		}
	}
	return false // malformed snapshots must never broaden admission
}

func applyOAuthInitialModels(input *CreateAccountInput, rules []OAuthModelMappingRule, selected []string) {
	if selected == nil {
		applyOAuthModelMappings(input, rules)
		return
	}
	// Read the original import scope before aliases are added. A selection can
	// narrow an explicit import whitelist, but cannot broaden it.
	input.Credentials = maps.Clone(input.Credentials)
	if input.Credentials == nil {
		input.Credentials = make(map[string]any)
	}
	if typed, ok := input.Credentials["model_mapping"].(map[string]string); ok {
		input.Credentials["model_mapping"] = asAnyModelMapping(typed)
	}
	original := &Account{Platform: input.Platform, Type: input.Type, Credentials: input.Credentials}
	mapping := rawUpstreamModelMapping(original)
	_, supplied := input.Credentials["model_mapping"]
	malformed := supplied && input.Credentials["model_mapping"] != nil && len(mapping) == 0
	if raw, ok := input.Credentials["model_mapping"].(map[string]any); ok && len(raw) == 0 {
		malformed = false
	}
	if raw, ok := input.Credentials["model_mapping"].(map[string]string); ok && len(raw) == 0 {
		malformed = false
	}
	eligible := make(map[string]bool, len(selected))
	for _, model := range selected {
		eligible[model] = !malformed && original.IsModelSupported(model)
		if len(mapping) > 0 && !original.IsOpenAIModelMappingAliases() {
			eligible[model] = eligible[model] && mappingSupportsRequestedModel(mapping, model)
		}
	}
	applyOAuthModelMappings(input, rules)
	// Use the same provider defaults/aliases as account editing. Convert typed
	// imports to the persisted JSON representation before resolving them.
	input.Credentials = maps.Clone(input.Credentials)
	if input.Credentials == nil {
		input.Credentials = make(map[string]any)
	}
	account := &Account{Platform: input.Platform, Type: input.Type, Credentials: input.Credentials}
	filtered := make(map[string]any)
	for _, source := range selected {
		target := account.GetMappedModel(source)
		// Provider defaults are not an explicit import whitelist. Preserve a
		// newly configured alias whose selected target the provider supports.
		if !eligible[source] && !malformed && len(mapping) == 0 && target != source && original.IsModelSupported(target) {
			eligible[source] = true
		}
		if !eligible[source] {
			continue
		}
		if slices.Contains(selected, target) {
			filtered[source] = target
		}
	}
	input.Credentials["model_mapping"] = filtered
	// Compact-only redirects must respect the same target boundary.
	compact := make(map[string]any)
	for source, raw := range asAnyModelMapping(input.Credentials["compact_model_mapping"]) {
		if target, ok := raw.(string); ok && eligible[source] && slices.Contains(selected, source) && slices.Contains(selected, target) {
			compact[source] = target
		}
	}
	if _, exists := input.Credentials["compact_model_mapping"]; exists {
		input.Credentials["compact_model_mapping"] = compact
	}
	input.Credentials[OAuthInitialModelSelectionKey] = append([]string{}, selected...)
	if input.Platform == PlatformOpenAI {
		input.Credentials[OpenAIModelMappingModeKey] = "whitelist"
	}
}

func asAnyModelMapping(raw any) map[string]any {
	switch mapping := raw.(type) {
	case map[string]any:
		return mapping
	case map[string]string:
		result := make(map[string]any, len(mapping))
		for source, target := range mapping {
			result[source] = target
		}
		return result
	}
	return nil
}
