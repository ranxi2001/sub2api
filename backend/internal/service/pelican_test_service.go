package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
)

// pelicanClaudeMaxTokens leaves room for thinking plus a complete HTML answer.
// Thinking counts toward max_tokens and cannot be turned off on Claude Opus 5.5,
// so the connection probe's 1024 tokens could run out before any text. 32000 fits
// the output limit of every Claude 4 and later model.
const pelicanClaudeMaxTokens = 32000

type pelicanTestContextKey struct{}

type pelicanTestOptions struct {
	prompt          string
	reasoningEffort string
	testChannel     string
	observeOnly     bool
}

func withPelicanTestOptions(ctx context.Context, options pelicanTestOptions) context.Context {
	ctx = context.WithValue(ctx, qualityProbeContextKey{}, true)
	return context.WithValue(ctx, pelicanTestContextKey{}, options)
}

func pelicanTestOptionsFromContext(ctx context.Context) (pelicanTestOptions, bool) {
	options, ok := ctx.Value(pelicanTestContextKey{}).(pelicanTestOptions)
	return options, ok
}

func isQualityObservation(ctx context.Context) bool {
	options, _ := pelicanTestOptionsFromContext(ctx)
	return options.observeOnly
}

// TestPelicanAccountConnection is the dedicated account test path for the
// Pelican UI. The existing /test endpoint deliberately keeps its historical
// probe payload; only this endpoint opts into the user prompt and reasoning.
func (s *AccountTestService) TestPelicanAccountConnection(c *gin.Context, accountID int64, modelID, prompt, reasoningEffort string) error {
	options, _ := pelicanTestOptionsFromContext(c.Request.Context())
	options.prompt = strings.TrimSpace(prompt)
	options.reasoningEffort = normalizePelicanReasoningEffort(reasoningEffort)
	ctx := withPelicanTestOptions(c.Request.Context(), options)
	c.Request = c.Request.WithContext(ctx)
	return s.TestAccountConnection(c, accountID, modelID, options.prompt, AccountTestModeDefault)
}

func normalizePelicanReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func pelicanTestRequested(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	_, ok := pelicanTestOptionsFromContext(c.Request.Context())
	return ok
}

func createPelicanClaudePayload(modelID, prompt, reasoningEffort string) (map[string]any, error) {
	payload, err := createTestPayload(modelID)
	if err != nil {
		return nil, err
	}
	payload["max_tokens"] = pelicanClaudeMaxTokens
	if effort := pelicanClaudeEffort(modelID, reasoningEffort); effort != "" {
		payload["output_config"] = map[string]any{"effort": effort}
		if claude.SupportsAdaptiveThinking(modelID) {
			payload["thinking"] = map[string]any{"type": "adaptive"}
		}
	}
	messages, ok := payload["messages"].([]map[string]any)
	if !ok || len(messages) == 0 {
		return payload, nil
	}
	content, ok := messages[0]["content"].([]map[string]any)
	if ok && len(content) > 0 {
		content[0]["text"] = promptOrDefault(prompt)
	}
	return payload, nil
}

// pelicanClaudeEffort maps the selected reasoning level to output_config.effort
// for models that accept that level; other models keep their default behavior.
func pelicanClaudeEffort(modelID, reasoningEffort string) string {
	effort := normalizePelicanReasoningEffort(reasoningEffort)
	if effort == "" {
		return ""
	}
	for _, level := range claude.EffortLevelsForModel(modelID) {
		if level == effort {
			return effort
		}
	}
	return ""
}

// pelicanClaudeStopFailure explains an answer that ended unfinished: thinking and
// the reply share max_tokens, and a refusal can arrive before any text.
func pelicanClaudeStopFailure(stopReason, category string) string {
	switch stopReason {
	case "max_tokens":
		return pelicanErrMaxTokens
	case "refusal":
		if category != "" {
			return fmt.Sprintf("%s (category: %s)", pelicanErrRefused, category)
		}
		return pelicanErrRefused
	}
	return ""
}

func createPelicanOpenAIPayload(modelID string, isOAuth bool, prompt, reasoningEffort string) map[string]any {
	payload := createOpenAITestPayload(modelID, isOAuth)
	input, ok := payload["input"].([]map[string]any)
	if ok && len(input) > 0 {
		content, ok := input[0]["content"].([]map[string]any)
		if ok && len(content) > 0 {
			content[0]["text"] = promptOrDefault(prompt)
		}
	}
	if effort := normalizePelicanReasoningEffort(reasoningEffort); effort != "" {
		payload["reasoning"] = map[string]any{"effort": effort}
	}
	return payload
}

func promptOrDefault(prompt string) string {
	if value := strings.TrimSpace(prompt); value != "" {
		return value
	}
	return "hi"
}
