package service

import (
	"encoding/json"
	"strings"
)

// A fetched or pinned native manifest is not evidence of the inference route's
// capabilities. Restrict models this group can send through BPS or a Chat
// Completions bridge, neither of which reads native encrypted agent payloads.
// Operate on the caller's copy and update its ETag after all group transforms.
func restrictExcelBPSCodexModelsManifest(body []byte, accounts []Account, group *Group) ([]byte, bool, error) {
	hasBPS := false
	for i := range accounts {
		hasBPS = hasBPS || accounts[i].IsExcelBPSEnabled() ||
			(accounts[i].IsOpenAI() && shouldForwardOpenAIResponsesViaRawChatCompletions(&accounts[i]))
	}
	if !hasBPS {
		return body, false, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, false, err
	}
	var models []json.RawMessage
	if err := json.Unmarshal(envelope["models"], &models); err != nil {
		return nil, false, err
	}
	changed := false
	for index, raw := range models {
		var model map[string]json.RawMessage
		if json.Unmarshal(raw, &model) != nil {
			continue
		}
		var slug string
		if json.Unmarshal(model["slug"], &slug) != nil || !codexGroupModelUsesExcelBPS(strings.TrimSpace(slug), accounts, group) {
			continue
		}
		updated := false
		for _, field := range []string{"multi_agent_version", "multi_agent_reasoning_effort"} {
			if string(model[field]) != "null" {
				model[field] = json.RawMessage("null")
				updated = true
			}
		}
		if updated {
			var err error
			models[index], err = json.Marshal(model)
			if err != nil {
				return nil, false, err
			}
			changed = true
		}
	}
	if !changed {
		return body, false, nil
	}
	var err error
	envelope["models"], err = json.Marshal(models)
	if err != nil {
		return nil, false, err
	}
	result, err := json.Marshal(envelope)
	return result, err == nil, err
}

func codexGroupModelUsesExcelBPS(model string, accounts []Account, group *Group) bool {
	if model == "" {
		return false
	}
	var groupID *int64
	if group != nil {
		groupID = &group.ID
	}
	routed := codexModelRoutingAccountIDs(group, model)
	for i := range accounts {
		account := &accounts[i]
		if len(routed) > 0 && !containsInt64(routed, account.ID) {
			continue
		}
		incompatible := account.IsExcelBPSEnabledForModel(model) ||
			(account.IsOpenAI() && shouldForwardOpenAIResponsesViaRawChatCompletions(account))
		if account.IsModelAllowedInGroup(groupID, model) && account.IsModelSupported(model) && incompatible {
			return true
		}
	}
	return false
}
