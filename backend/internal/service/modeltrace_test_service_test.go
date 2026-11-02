//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/modeltrace"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type modelTraceRepo struct {
	AccountRepository
	account   *Account
	mutations int
}

func (r *modelTraceRepo) GetByID(context.Context, int64) (*Account, error) { return r.account, nil }
func (r *modelTraceRepo) SetError(context.Context, int64, string) error    { r.mutations++; return nil }

func TestModelTraceNativeProtocolsAndThreeQuestionBudget(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			text := strings.Repeat("17,83,247,", 100)
			encoded, _ := json.Marshal(text)
			stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":" + string(encoded) + "}\n\ndata: {\"type\":\"response.completed\"}\n\n"
			account := &Account{ID: 77, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "mock-only", "base_url": "https://example.com", "model_mapping": map[string]any{"public-model": "gpt-6-astra"}}}
			if protocol == "chat_completions" {
				account.Extra = map[string]any{"openai_responses_supported": false}
				stream = "data: {\"choices\":[{\"delta\":{\"content\":" + string(encoded) + "},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			}
			if protocol == "messages" {
				account.Platform = PlatformAnthropic
				account.Credentials["model_mapping"] = map[string]any{"public-model": "claude-sonnet-4-6"}
				stream = "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":" + string(encoded) + "}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
			}
			repo := &modelTraceRepo{account: account}
			upstream := &queuedHTTPUpstream{}
			for range 3 {
				upstream.responses = append(upstream.responses, newJSONResponse(http.StatusOK, stream))
			}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: &config.Config{}}
			c, recorder := newTestContext()
			require.NoError(t, svc.TestModelTraceAccountConnection(c, 77, "public-model", ""))
			require.Len(t, upstream.requests, 3)
			require.Zero(t, repo.mutations, "attribution must not isolate the account")
			var completed ModelTraceResult
			for _, line := range strings.Split(recorder.Body.String(), "\n") {
				var event struct {
					Type string          `json:"type"`
					Data json.RawMessage `json:"data"`
				}
				if data, ok := strings.CutPrefix(line, "data:"); ok && json.Unmarshal([]byte(data), &event) == nil && event.Type == "modeltrace_complete" {
					require.NoError(t, json.Unmarshal(event.Data, &completed))
				}
			}
			require.Equal(t, "complete", completed.Status)
			require.Len(t, completed.Samples, 3)
			require.Equal(t, 3, completed.Analysis.UsedOutputs)
			prompts := make(map[string]bool)
			for i, request := range upstream.requests {
				body, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				var payload map[string]any
				require.NoError(t, json.Unmarshal(body, &payload))
				require.NotContains(t, payload, "system")
				require.NotContains(t, payload, "instructions")
				require.NotContains(t, payload, "temperature")
				require.NotContains(t, string(body), "HTML")
				require.Equal(t, protocol, completed.Samples[i].Environment.Protocol)
				require.NotEmpty(t, completed.Samples[i].MappedModel)
				prompts[completed.Samples[i].Challenge.Prompt] = true
			}
			require.Len(t, prompts, 3)
		})
	}
}

func TestModelTraceCancellationDoesNotStartRemainingQuestions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls, emissions := 0, 0
	challenges := make([]modeltrace.Challenge, 3)
	err := runModelTraceSamples(ctx, challenges, func(ctx context.Context, _ modeltrace.Challenge) ModelTraceSample {
		calls++
		_, hasDeadline := ctx.Deadline()
		require.True(t, hasDeadline)
		cancel()
		return ModelTraceSample{}
	}, func(ModelTraceSample) { emissions++ })
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
	require.Zero(t, emissions)
}

func TestModelTraceIncompleteAnswersAndRefusalsAreExcluded(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			c, rec := newTestContext()
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), modelTraceContextKey{}, modelTraceEnvironment{Protocol: protocol}))
			svc := &AccountTestService{}
			var err error
			switch protocol {
			case "responses":
				err = svc.processOpenAIStream(c, strings.NewReader("data: {\"type\":\"response.done\",\"response\":{\"status\":\"incomplete\"}}\n\n"))
			case "chat_completions":
				err = svc.processOpenAIChatCompletionsStream(c, strings.NewReader("data: {\"choices\":[{\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"), nil)
			case "messages":
				c.Request = c.Request.WithContext(withPelicanTestOptions(c.Request.Context(), pelicanTestOptions{}))
				err = svc.processClaudeStream(c, strings.NewReader("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
			}
			require.Error(t, err)
			_, message := parsePelicanOutput(rec.Body.String())
			require.NotEmpty(t, message)
		})
	}
	result := ModelTraceResult{Samples: []ModelTraceSample{
		{Accepted: true, Text: strings.Repeat("17,", 200), Challenge: modeltrace.Challenge{ExpectedCount: 300}},
		{Accepted: false, Text: strings.Repeat("83,", 300), Error: "generation_failed"},
		{Accepted: false, Text: "refused", Error: "too_few_numbers"},
	}}
	completeModelTraceResult(&result)
	require.Equal(t, "partial", result.Status)
	require.Equal(t, 1, result.Analysis.UsedOutputs)
	completeModelTraceResult(&ModelTraceResult{})
}

func TestModelTraceBareEOFIsNotAnAnthropicCompletion(t *testing.T) {
	c, rec := newTestContext()
	ctx := context.WithValue(c.Request.Context(), modelTraceContextKey{}, modelTraceEnvironment{Protocol: "messages"})
	c.Request = c.Request.WithContext(withPelicanTestOptions(ctx, pelicanTestOptions{}))
	stream := "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"17,83,247\"}}\n\n"
	err := (&AccountTestService{}).processClaudeStream(c, strings.NewReader(stream))
	require.Error(t, err)
	_, message := parsePelicanOutput(rec.Body.String())
	require.NotEmpty(t, message)
}

func TestModelTraceOAuthInstructionsAreRetainedAndOtherRoutesRejected(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic} {
		account := &Account{Platform: platform, Type: AccountTypeOAuth}
		env, err := modelTraceEnvironmentForAccount(account, "gpt-6-astra")
		require.NoError(t, err)
		ctx := context.WithValue(context.Background(), modelTraceContextKey{}, env)
		payload := map[string]any{"system": "required", "instructions": "required", "temperature": 1}
		applyModelTracePayload(ctx, payload)
		require.Equal(t, "required", payload["system"])
		require.Equal(t, "required", payload["instructions"])
		require.NotEqual(t, "none", env.SystemPrompt)
	}
	_, err := modelTraceEnvironmentForAccount(&Account{Platform: PlatformGemini, Type: AccountTypeAPIKey}, "gemini")
	require.Error(t, err)
	svc := &AccountTestService{accountRepo: &modelTraceRepo{account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}}}
	svc.modelTraceAccounts.Store(int64(77), struct{}{})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	require.Error(t, svc.TestModelTraceAccountConnection(c, 77, "gpt-6-astra", ""))
	require.Empty(t, rec.Body.String(), "busy validation must precede SSE headers")
}
