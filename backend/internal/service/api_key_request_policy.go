package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type apiKeyMappedModelContextKey struct{}

// WithAPIKeyMappedModel records an applied request rewrite, independently of
// later key configuration changes and channel/account mappings.
func WithAPIKeyMappedModel(ctx context.Context, model string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if model == "" {
		return ctx
	}
	return context.WithValue(ctx, apiKeyMappedModelContextKey{}, model)
}

func APIKeyMappedModelFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	model, _ := ctx.Value(apiKeyMappedModelContextKey{}).(string)
	return model
}

// MapAPIKeyModel applies exactly one administrator-defined alias. It never
// mutates a cached key or follows a mapping chain.
func MapAPIKeyModel(key *APIKey, model string) string {
	if key != nil {
		if mapped := key.ModelMapping[model]; mapped != "" {
			return mapped
		}
	}
	return model
}

// APIKeyModelCandidates keeps the routed model first for availability probes,
// while retaining every client alias for group/user permission checks.
func APIKeyModelCandidates(key *APIKey, models []string) []string {
	if key == nil || len(key.ModelMapping) == 0 {
		return models
	}
	result := make([]string, 0, 2*len(models))
	seen := make(map[string]bool, 2*len(models))
	for _, model := range models {
		for _, candidate := range []string{MapAPIKeyModel(key, model), model} {
			if candidate != "" && !seen[candidate] {
				result = append(result, candidate)
				seen[candidate] = true
			}
		}
	}
	return result
}

// IsAPIKeyModelPolicyPath limits key aliases to text inference protocols. Other
// endpoints (including image, audio and model discovery) retain their semantics.
func IsAPIKeyModelPolicyPath(path string) bool {
	return strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/responses/compact") ||
		strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/messages") ||
		strings.HasSuffix(path, "/messages/count_tokens")
}

// ApplyAPIKeyOpenAIReasoningEffortPolicy injects the key's fixed effort, then
// enforces the group's ceiling. A fixed effort replaces ordinary group remaps;
// an empty setting retains the complete existing group policy.
func ApplyAPIKeyOpenAIReasoningEffortPolicy(body []byte, forced, maxEffort string, mappings []ReasoningEffortMapping, overLimit string, requestModels ...string) ([]byte, bool, error) {
	if forced == "" {
		return ApplyOpenAIReasoningEffortPolicy(body, maxEffort, mappings, overLimit, requestModels...)
	}
	view := openAIRequestPayloadView(body)
	payload := []byte(view.Raw)
	path := "reasoning.effort"
	if view.Get("messages").Exists() {
		path = "reasoning_effort"
	}
	updated, err := setAPIKeyCanonicalJSONField(payload, strings.Split(path, "."), forced)
	if err != nil {
		return body, false, err
	}
	// Some compatibility clients send both forms. Normalize every present
	// effort field so a later adapter cannot recover the client's old value.
	for _, other := range []string{"reasoning.effort", "reasoning_effort", "output_config.effort"} {
		if other != path && apiKeyJSONFieldExists(updated, strings.Split(other, ".")) {
			updated, err = setAPIKeyCanonicalJSONField(updated, strings.Split(other, "."), forced)
			if err != nil {
				return body, false, err
			}
		}
	}
	if capped, changed, capErr := ApplyOpenAIReasoningEffortPolicy(updated, maxEffort, nil, overLimit, requestModels...); capErr != nil {
		return body, false, capErr
	} else if changed {
		updated = capped
	}
	root := gjson.ParseBytes(body)
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(root.Get("type").String())), "response.") && root.Get("response").IsObject() {
		updated, err = sjson.SetBytes(body, "response", json.RawMessage(updated))
		if err != nil {
			return body, false, err
		}
	}
	return updated, true, nil
}

func apiKeyJSONFieldExists(body []byte, path []string) bool {
	found := false
	gjson.ParseBytes(body).ForEach(func(key, value gjson.Result) bool {
		if strings.EqualFold(key.String(), path[0]) && (len(path) == 1 || apiKeyJSONFieldExists([]byte(value.Raw), path[1:])) {
			found = true
			return false
		}
		return true
	})
	return found
}

// Canonical keys prevent case-insensitive struct decoders or duplicate JSON
// members from restoring a client-supplied effort in downstream adapters.
func setAPIKeyCanonicalJSONField(body []byte, path []string, value string) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, err
	}
	if object == nil {
		object = make(map[string]json.RawMessage)
	}
	var nested json.RawMessage
	for field, raw := range object {
		if strings.EqualFold(field, path[0]) {
			if field == path[0] || nested == nil {
				nested = raw
			}
			delete(object, field)
		}
	}
	var encoded []byte
	var err error
	if len(path) == 1 {
		encoded, err = json.Marshal(value)
	} else {
		if !gjson.ParseBytes(nested).IsObject() {
			nested = json.RawMessage(`{}`)
		}
		encoded, err = setAPIKeyCanonicalJSONField(nested, path[1:], value)
	}
	if err != nil {
		return nil, err
	}
	object[path[0]] = encoded
	return json.Marshal(object)
}
