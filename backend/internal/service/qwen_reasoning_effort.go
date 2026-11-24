package service

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Model Studio's published Chat/Responses effort scales differ across model generations.
// Match the mapped, published API ID; e.g. realtime and Qwen4 preview have separate contracts.
func qwenEffortModel(model string) bool {
	switch model {
	case "qwen4-max", "qwen3.8-max", "qwen3.8-max-0902", "qwen3.8-max-2026-09-02",
		"qwen3.8-flash", "qwen3.8-2.4t-a95b", "qwen3.8-27b", "qwen3.8-omni-flash":
		return true
	}
	return false
}

func qwenEffectiveEffort(model, effort string) string {
	switch effort {
	case "minimal":
		return "low"
	case "max":
		return "xhigh"
	case "high":
		if model != "qwen4-max" {
			return "xhigh"
		}
	case "medium":
		if model == "qwen4-max" {
			return "high"
		}
	}
	return effort
}

// Capture the original request first. Canonicalize the selected account's copy before
// routing so both the outgoing request and its usage result see the same effort.
func normalizeQwenRequestEffort(account *Account, body []byte, defaultModel string, path string) []byte {
	if account == nil || account.Platform != PlatformQwen {
		return body
	}
	model := resolveMappedUpstreamModel(account, body, defaultModel)
	if !qwenEffortModel(model) {
		return body
	}
	// OpenAI-compatible clients may put the Responses spelling on a Chat
	// request or the flat Chat alias on a Responses request.
	if path != "output_config.effort" {
		path = "reasoning.effort"
		if !gjson.GetBytes(body, path).Exists() {
			path = "reasoning_effort"
		}
	}
	effort := gjson.GetBytes(body, path)
	if effort.Type != gjson.String {
		return body
	}
	target := qwenEffectiveEffort(model, effort.Str)
	if target != effort.Str {
		if updated, err := sjson.SetBytes(body, path, target); err == nil {
			return updated
		}
	}
	return body
}

// The generic OpenAI-to-Anthropic bridge emits Claude's max and synthesizes a
// token budget. For an explicit Qwen effort send its native Messages setting.
func qwenBridgedMessagesEffort(account *Account, body, original []byte) ([]byte, error) {
	if account.Platform != PlatformQwen {
		return body, nil
	}
	model := gjson.GetBytes(body, "model").Str
	if !qwenEffortModel(model) {
		return body, nil
	}
	effort := gjson.GetBytes(original, "reasoning.effort").Str
	if effort == "" {
		effort = gjson.GetBytes(original, "reasoning_effort").Str
	}
	if effort == "" {
		return body, nil
	}
	if effort == "none" {
		updated, err := sjson.DeleteBytes(body, "output_config.effort")
		if err != nil {
			return nil, err
		}
		return sjson.SetBytes(updated, "thinking", map[string]string{"type": "disabled"})
	}
	updated, err := sjson.DeleteBytes(body, "thinking.budget_tokens")
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(updated, "output_config.effort", qwenEffectiveEffort(model, effort))
}

func nativeMessagesUsageEffort(account *Account, body []byte, model string) *string {
	if account.Platform == PlatformQwen && qwenEffortModel(model) && gjson.GetBytes(body, "thinking.type").Str == "disabled" {
		effort := "none"
		return &effort
	}
	return ApplyThinkingEnabledFallback(NormalizeClaudeOutputEffort(gjson.GetBytes(body, "output_config.effort").String()), body, model)
}
