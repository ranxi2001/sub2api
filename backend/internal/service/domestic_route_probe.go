package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const domesticRouteBudget = 3 * time.Second

// The database owns the hourly claim across all background-job instances.
type DomesticRouteClaim struct {
	AccountID int64
	ClaimedAt time.Time
}

type domesticRouteStore interface {
	ClaimDomesticRouteProbes(context.Context) ([]DomesticRouteClaim, error)
	ApplyDomesticRouteProbe(context.Context, DomesticRouteClaim, bool) error
}

func (s *ScheduledTestRunnerService) runDomesticRouteProbes(ctx context.Context) {
	if s.accountTestSvc == nil {
		return
	}
	repo, ok := s.accountTestSvc.accountRepo.(domesticRouteStore)
	if !ok {
		return
	}
	claims, err := repo.ClaimDomesticRouteProbes(ctx)
	if err != nil {
		logger.LegacyPrintf("service.domestic_route", "claim probes: %v", err)
		return
	}
	for _, claim := range claims {
		if ctx.Err() != nil {
			return
		}
		account, err := s.accountTestSvc.accountRepo.GetByID(ctx, claim.AccountID)
		if err != nil || account.Extra["domestic_route_auto_schedule"] != true {
			continue
		}
		model := domesticRouteModel(account)
		if model == "" {
			continue
		}
		// Preserve independent expiry, quota, cooldown and performance gates.
		eligible := *account
		eligible.Schedulable = true
		if !eligible.IsSchedulable() {
			continue
		}
		first, measured := s.accountTestSvc.probeDomesticRoute(ctx, account, model)
		if !measured || ctx.Err() != nil {
			continue
		}
		enable := domesticRouteFast(first)
		if err := repo.ApplyDomesticRouteProbe(ctx, claim, enable); err != nil {
			logger.LegacyPrintf("service.domestic_route", "apply account=%d: %v", claim.AccountID, err)
			continue
		}
		logger.LegacyPrintf("service.domestic_route", "account=%d model=%s first_text_ms=%d target_schedulable=%t", claim.AccountID, model, first.Milliseconds(), enable)
	}
}

func domesticRouteFast(first time.Duration) bool {
	return first >= 0 && first < domesticRouteBudget
}

func domesticRouteModel(a *Account) string {
	if a.Type != AccountTypeAPIKey {
		return ""
	}
	var candidates []string
	switch a.Platform {
	case PlatformDeepseek:
		candidates = []string{"deepseek-flash", "deepseek-v4.1-flash", "deepseek-chat", "deepseek-pro"}
	case PlatformQwen:
		candidates = []string{"qwen3.8-flash", DefaultQwenTestModel, "qwen-plus", "qwen-turbo"}
	case PlatformKimi:
		candidates = []string{"kimi-k3", "kimi-for-coding", "kimi-k2.6", "moonshot-v1-8k"}
	case PlatformZhipu:
		candidates = []string{"glm-5.3-flash", "glm-5.3", "glm-5", "glm-4-flash"}
	case PlatformMiniMax:
		candidates = []string{"MiniMax-M3", "MiniMax-M2.7", "MiniMax-M2.5"}
	}
	for _, m := range candidates {
		if a.IsModelSupported(m) {
			return a.GetMappedModel(m)
		}
	}
	// An administrator may have exposed a preferred model only under an alias.
	for _, model := range candidates {
		for _, target := range a.GetModelMapping() {
			if target == model {
				return model
			}
		}
	}
	return ""
}

// Return a negative duration for an attempted request without visible output.
// Local configuration failures and background shutdown produce no decision.
func (s *AccountTestService) probeDomesticRoute(ctx context.Context, a *Account, model string) (time.Duration, bool) {
	protocol := a.GetAPIProtocol()
	base := a.GetOpenAIBaseURL()
	if protocol == APIProtocolAnthropic {
		base = a.GetAnthropicProtocolBaseURL()
	}
	base, err := s.validateUpstreamBaseURL(base)
	key := a.GetOpenAIProtocolAPIKey()
	if err != nil || key == "" || (a.ProxyID != nil && a.Proxy == nil) {
		return 0, false
	}
	prompt := "Reply only with OK."
	payload := createOpenAIChatCompletionsTestPayload(model, prompt)
	payload["max_tokens"] = 16
	url := buildOpenAIChatCompletionsURL(base)
	switch protocol {
	case APIProtocolAnthropic:
		url = nativeAnthropicMessagesURL(a, base)
	case APIProtocolResponses:
		url = buildOpenAIResponsesURLForPlatform(a.Platform, base)
		payload = map[string]any{"model": model, "stream": true, "input": prompt, "max_output_tokens": 16}
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return 0, false
	}
	attempt, cancel := context.WithTimeout(ctx, domesticRouteBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(WithHTTPUpstreamProfile(attempt, HTTPUpstreamProfileOpenAI), http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return 0, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if protocol == APIProtocolAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
		setAnthropicAPIKeyAuthHeader(req.Header, a, key, base)
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
		applyOpenAIAPIKeyIdentityHeaders(req.Header, a, a.GetOpenAIUserAgent())
	}
	a.ApplyHeaderOverrides(req.Header)
	start := time.Now()
	resp, err := s.doCNProviderAdaptiveRequest(req, a)
	if err != nil {
		return -time.Millisecond, ctx.Err() == nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return -time.Millisecond, true
	}
	if !domesticRouteVisibleText(resp.Body, protocol, a, model) {
		return -time.Millisecond, true
	}
	return time.Since(start), true
}

func domesticRouteVisibleText(body io.Reader, protocol string, a *Account, model string) bool {
	scanner := bufio.NewScanner(io.LimitReader(body, 1<<20))
	scanner.Buffer(make([]byte, 4096), 256<<10)
	lines := newDeepSeekChatSSEPrefixScanner(scanner, a, model)
	for lines.Scan() {
		data, ok := strings.CutPrefix(strings.TrimSpace(lines.Text()), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			return false
		}
		var event struct {
			Type    string
			Delta   json.RawMessage
			Error   json.RawMessage
			Choices []struct{ Delta struct{ Content string } }
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil || event.Type == "error" || (len(event.Error) > 0 && string(event.Error) != "null") {
			return false
		}
		switch protocol {
		case APIProtocolResponses:
			var text string
			if event.Type == "response.output_text.delta" && json.Unmarshal(event.Delta, &text) == nil && strings.TrimSpace(text) != "" {
				return true
			}
		case APIProtocolAnthropic:
			var delta struct{ Type, Text string }
			if event.Type == "content_block_delta" && json.Unmarshal(event.Delta, &delta) == nil && delta.Type == "text_delta" && strings.TrimSpace(delta.Text) != "" {
				return true
			}
		default:
			for _, choice := range event.Choices {
				if strings.TrimSpace(choice.Delta.Content) != "" {
					return true
				}
			}
		}
	}
	return false
}
