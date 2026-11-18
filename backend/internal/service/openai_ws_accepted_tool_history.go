package service

import "github.com/tidwall/gjson"

// Paired client history accepted by a completed generation is context, not
// proof that this connection executed the old tool. It never enters shared
// provenance and only permits the same unchanged pair on this bridge/account.
type openAIWSAcceptedToolHistory struct {
	fingerprint string
	result      [32]byte
	account     int64
	config      [32]byte
}

func openAIWSToolResultDigest(item gjson.Result) [32]byte {
	return openAIAgentRecoveryInputDigest([]byte(`{"input":[` + item.Raw + `]}`))
}

func acceptedOpenAIWSToolHistory(source []byte, account *Account, model string) map[string]openAIWSAcceptedToolHistory {
	if account == nil || !account.IsOpenAIApiKey() || len(source) > 8<<20 ||
		gjson.GetBytes(source, "previous_response_id").String() != "" || gjson.GetBytes(source, "conversation").Exists() ||
		len(openAIAgentRecoveryItemDigests(source)) != 0 || !openAIEncryptedJSONUnambiguous(source) {
		return nil
	}
	if _, reason := inspectOpenAIWSCompletedTools(source); reason != "" {
		return nil
	}
	normalized, _, err := sanitizeOpenAIResponsesInputItemIDs(source)
	if err != nil {
		return nil
	}
	results := map[string]gjson.Result{}
	for _, item := range gjson.GetBytes(normalized, "input").Array() {
		if kind := item.Get("type").String(); kind == "function_call_output" || kind == "custom_tool_call_output" {
			results[item.Get("call_id").String()] = item
		}
	}
	accepted := map[string]openAIWSAcceptedToolHistory{}
	for _, item := range gjson.GetBytes(normalized, "input").Array() {
		kind := item.Get("type").String()
		if kind != "function_call" && kind != "custom_tool_call" {
			continue
		}
		fingerprint, valid := openAIToolFingerprint(item)
		id := item.Get("call_id").String()
		result, found := results[id]
		if !valid || !found || result.Get("type").String() != kind+"_output" || len(accepted) >= 1024 {
			return nil
		}
		accepted[model+"\x00"+id] = openAIWSAcceptedToolHistory{fingerprint: fingerprint, result: openAIWSToolResultDigest(result), account: account.ID, config: openAIAgentRecoveryConfig(account, model)}
	}
	return accepted
}

func acceptsPriorToolPair(acceptedTools map[string]openAIWSAcceptedToolHistory, account *Account, model string, call gjson.Result, history []byte) bool {
	accepted, ok := acceptedTools[model+"\x00"+call.Get("call_id").String()]
	fingerprint, valid := openAIToolFingerprint(call)
	if !ok || !valid || accepted.account != account.ID || accepted.config != openAIAgentRecoveryConfig(account, model) || accepted.fingerprint != fingerprint {
		return false
	}
	for _, item := range gjson.GetBytes(history, "input").Array() {
		if item.Get("type").String() == call.Get("type").String()+"_output" && item.Get("call_id").String() == call.Get("call_id").String() {
			return accepted.result == openAIWSToolResultDigest(item)
		}
	}
	return false
}
