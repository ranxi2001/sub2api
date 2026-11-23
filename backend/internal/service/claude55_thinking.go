package service

import (
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// adaptThinkingToClaude55 replaces a thinking mode that a Claude 5.5 model
// rejects with the nearest mode it accepts: enabled becomes adaptive without
// budget_tokens, and on Sonnet 5.5 disabled becomes between_tools. Other modes
// are left for validateClaude55Request to judge.
func adaptThinkingToClaude55(body []byte, model string) []byte {
	if !isClaude55SignedThinkingModel(model) {
		return body
	}
	switch gjson.GetBytes(body, "thinking.type").String() {
	case "enabled":
		body, _ = sjson.SetBytes(body, "thinking.type", "adaptive")
		body, _ = sjson.DeleteBytes(body, "thinking.budget_tokens")
	case "disabled":
		if claude.IsSonnet55(model) {
			body, _ = sjson.SetBytes(body, "thinking.type", "between_tools")
		}
	}
	return body
}

// prepareClaudeMessagesRequest validates the mapped upstream model. Native
// API-key relays use Claude's adaptive mode for legacy Opus/Fable thinking.
// The attempt owns its body so a later account still sees the caller's input.
func prepareClaudeMessagesRequest(account *Account, parsed *ParsedRequest, model string) (*ParsedRequest, error) {
	if account == nil || account.Platform != PlatformAnthropic {
		return parsed, nil
	}
	body := parsed.Body.Bytes()
	if account.Type == AccountTypeAPIKey && (claude.IsOpus55(model) || claude.IsFable52(model)) &&
		gjson.GetBytes(body, "thinking.type").String() == "enabled" {
		base, err := url.Parse(getAnthropicGatewayBaseURL(account))
		if err == nil && base.Hostname() != "" && !strings.EqualFold(strings.TrimSuffix(base.Hostname(), "."), "api.anthropic.com") {
			// CloneForBody clears the acceptance hook for a new scheduler attempt.
			// Here we are already inside that attempt, so retain its release hook.
			adapted, err := parsed.CloneForBody(adaptThinkingToClaude55(body, model))
			if err != nil {
				return nil, err
			}
			adapted.OnUpstreamAccepted = parsed.OnUpstreamAccepted
			parsed = adapted
		}
	}
	return parsed, validateClaude55Request(parsed.Body.Bytes(), model)
}
