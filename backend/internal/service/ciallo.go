package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ciallo-v1 recognizes the three greeting forms in the field examples published
// in narutoFyy/sub2-fwq2@48e1184. Require the whole greeting at the start;
// a quoted word, plain emoji, or an unfinished face is insufficient.
var cialloGreeting = regexp.MustCompile(`(?i)^ciallo[!！~～[:blank:]]*(?:[(（]∠・ω[<＜][[:blank:]]*[)）]⌒[☆★]|[(（]≧▽≦[)）]|[(（]\^_\^[)）])`)

const (
	QualityCialloQuestionKind = "ciallo"
	cialloTemplateVersion     = "ciallo-v1"
	cialloJudgeVersion        = "greeting-v1"
)

func matchedCialloGreeting(answer string) string {
	return cialloGreeting.FindString(strings.TrimSpace(answer))
}

type CialloResult struct {
	Model           string    `json:"model"`
	MappedModel     string    `json:"mapped_model"`
	ReasoningEffort string    `json:"reasoning_effort"`
	Protocol        string    `json:"protocol"`
	TemplateVersion string    `json:"template_version"`
	JudgeVersion    string    `json:"judge_version"`
	StartedAt       time.Time `json:"started_at"`
	DurationMs      int64     `json:"duration_ms"`
	Outcome         string    `json:"outcome"`
	Reason          string    `json:"reason,omitempty"`
	// Return only the recognized greeting. Never retain arbitrary upstream text.
	Greeting string `json:"greeting,omitempty"`
}

// Result-only, owned by one scheduled sample. Adapters that cannot confirm a
// normal upstream finish leave the sample unassessable.
type CialloEvidence struct {
	TemplateVersion                string `json:"template_version"`
	JudgeVersion                   string `json:"judge_version"`
	Protocol                       string `json:"protocol,omitempty"`
	complete, interrupted, endTurn bool
}
type cialloEvidenceKey struct{}

func (e *CialloEvidence) observe(data gjson.Result, eventType string) {
	switch e.Protocol {
	case "openai":
		switch eventType {
		case "response.completed", "response.done":
			status := data.Get("response.status").String()
			e.complete = status == "" || status == "completed"
			e.interrupted = e.interrupted || !e.complete || data.Get("response.incomplete_details").IsObject()
		case "response.incomplete", "response.failed", "error":
			e.interrupted = true
		}
	case "anthropic":
		if reason := data.Get("delta.stop_reason").String(); reason != "" {
			e.endTurn = reason == "end_turn" || reason == "stop_sequence"
		}
		if eventType == "message_stop" {
			e.complete = e.endTurn
		}
	case "chat":
		if reason := data.Get("choices.0.finish_reason").String(); reason != "" {
			e.complete = reason == "stop"
			e.interrupted = e.interrupted || !e.complete
		}
	case "gemini":
		if reason := data.Get("candidates.0.finishReason").String(); reason != "" {
			e.complete = reason == "STOP"
			e.interrupted = e.interrupted || !e.complete
		}
	}
}

type cialloContextKey struct{}

func isCialloRequest(ctx context.Context) bool {
	active, _ := ctx.Value(cialloContextKey{}).(bool)
	return active
}

func (s *AccountTestService) RunCiallo(ctx context.Context, accountID int64, model, effort string) (*CialloResult, error) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	env, err := modelTraceEnvironmentForAccount(account, model)
	if err != nil || env.Protocol != "responses" || account.IsSIWCEnabled() || account.AllowsExtraImageModel(model) || account.AllowsExtraImageModel(account.GetMappedModel(model)) {
		return nil, infraerrors.BadRequest("CIALLO_UNSUPPORTED", "Ciallo requires a native OpenAI Responses text account")
	}
	if _, busy := s.cialloAccounts.LoadOrStore(accountID, struct{}{}); busy {
		return nil, infraerrors.Conflict("CIALLO_BUSY", "A Ciallo test is already running for this account")
	}
	defer s.cialloAccounts.Delete(accountID)
	result := &CialloResult{Model: model, MappedModel: account.GetMappedModel(model), Protocol: env.Protocol, ReasoningEffort: effort,
		TemplateVersion: cialloTemplateVersion, JudgeVersion: cialloJudgeVersion, StartedAt: time.Now(), Outcome: "unassessable"}
	ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, cialloContextKey{}, true)
	// Reuse the native text diagnostic payload (OAuth-required instructions only).
	ctx = context.WithValue(ctx, modelTraceContextKey{}, env)
	ctx = withPelicanTestOptions(ctx, pelicanTestOptions{prompt: "ciallo", reasoningEffort: effort, observeOnly: true})
	w := &pelicanRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	c, _ := gin.CreateTestContext(w)
	c.Request = (&http.Request{Header: make(http.Header)}).WithContext(ctx)
	err = s.testOpenAIAccountConnection(c, account, model, "ciallo", AccountTestModeDefault)
	output, failure := parsePelicanOutput(w.Body.String())
	result.DurationMs = time.Since(result.StartedAt).Milliseconds()
	switch {
	case w.overflow:
		result.Reason = "output_limit"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.Reason = "timeout"
	case ctx.Err() != nil:
		result.Reason = "cancelled"
	case err != nil || failure != "":
		result.Reason = cialloFailure(failure)
	case strings.TrimSpace(output) == "":
		result.Reason = "empty"
	default:
		result.Greeting = matchedCialloGreeting(output)
		result.Outcome = "unmatched"
		if result.Greeting != "" {
			result.Outcome = "matched"
		}
	}
	return result, nil
}

func cialloFailure(message string) string {
	switch {
	case strings.HasPrefix(message, "API returned 401:"), strings.HasPrefix(message, "API returned 403:"), message == "No access token available", message == "No API key available":
		return "auth"
	case strings.HasPrefix(message, "API returned 402:"):
		return "balance"
	case strings.HasPrefix(message, "API returned 429:"):
		return "limited"
	case strings.Contains(message, "Stream ended before"), message == "Ciallo generation incomplete":
		return "incomplete"
	default:
		return "request_failed"
	}
}
