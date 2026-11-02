//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPelicanTestUsageStreams(t *testing.T) {
	svc := &AccountTestService{}
	cases := []struct {
		name      string
		process   func(*gin.Context, io.Reader) error
		events    []string
		want      UsageTokens
		partial   bool
		wantError bool
	}{
		{
			name: "anthropic merges start and cumulative delta with split cache writes", process: svc.processClaudeStream,
			events: []string{
				`{"type":"message_start","message":{"model":"claude-sonnet-4","usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":60,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":20}}}}`,
				`{"type":"message_delta","usage":{"output_tokens":200}}`,
				`{"type":"message_stop"}`,
			},
			want: UsageTokens{InputTokens: 100, OutputTokens: 200, CacheReadTokens: 60, CacheCreationTokens: 40, CacheCreation5mTokens: 20, CacheCreation1hTokens: 20},
		},
		{
			name: "responses subtracts cache hits and counts reasoning once", process: svc.processOpenAIStream,
			events: []string{
				`{"type":"response.in_progress","response":{"usage":{"input_tokens":1000,"output_tokens":10,"input_tokens_details":{"cached_tokens":800}}}}`,
				`{"type":"response.completed","response":{"usage":{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":800},"output_tokens_details":{"reasoning_tokens":150}}}}`,
			},
			want: UsageTokens{InputTokens: 200, OutputTokens: 200, CacheReadTokens: 800},
		},
		{
			name: "chat reads usage-only final chunk", process: func(c *gin.Context, body io.Reader) error {
				return svc.processOpenAIChatCompletionsStream(c, body, nil)
			},
			events: []string{
				`{"choices":[{"delta":{"content":"<svg/>"},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":200,"prompt_tokens_details":{"cached_tokens":800}}}`,
				`[DONE]`,
			},
			want: UsageTokens{InputTokens: 200, OutputTokens: 200, CacheReadTokens: 800},
		},
		{
			name: "gemini CLI includes thinking and subtracts cached input", process: svc.processGeminiStream,
			events: []string{`{"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":300,"cachedContentTokenCount":100,"candidatesTokenCount":40,"thoughtsTokenCount":20}}}`},
			want:   UsageTokens{InputTokens: 200, OutputTokens: 60, CacheReadTokens: 100},
		},
		{
			name: "adaptive anthropic records its separate request", process: svc.processCNProviderAdaptiveAnthropicStream,
			events: []string{`{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":0}}}`, `{"type":"message_stop"}`},
			want:   UsageTokens{InputTokens: 100},
		},
		{
			name: "interrupted generation retains known input cost as partial", process: svc.processClaudeStream,
			events: []string{`{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":0}}}`, `{"type":"error","error":{"message":"interrupted"}}`},
			want:   UsageTokens{InputTokens: 100}, partial: true, wantError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := &pelicanTestUsageCollector{model: "claude-sonnet-4"}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(context.WithValue(context.Background(), pelicanTestUsageKey{}, collector))
			err := tc.process(c, strings.NewReader("data: "+strings.Join(tc.events, "\n\ndata: ")+"\n\n"))
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, collector.requests, 1)
			require.Equal(t, tc.want, collector.requests[0].tokens)
			cost, partial := collector.cost(newTestBillingService(), &Account{Extra: map[string]any{AccountCostMultiplierExtraKey: 0.25}})
			require.NotNil(t, cost)
			require.Positive(t, *cost)
			require.Equal(t, tc.partial, partial)
		})
	}
}

func TestPelicanTestUsageUnknownAndZero(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		model   string
		unknown bool
	}{
		{"missing usage", `{"type":"response.completed"}`, "claude-sonnet-4", true},
		{"explicit zero usage", `{"type":"response.completed","response":{"usage":{"input_tokens":0,"output_tokens":0}}}`, "claude-sonnet-4", false},
		{"missing pricing", `{"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":50}}}`, "unpriced-test-model", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := &pelicanTestUsageCollector{model: tc.model}
			u := startPelicanTestUsage(context.WithValue(context.Background(), pelicanTestUsageKey{}, collector), "openai")
			u.read(tc.raw)
			cost, partial := collector.cost(newTestBillingService(), &Account{})
			if tc.unknown {
				require.Nil(t, cost)
				require.True(t, partial)
			} else {
				require.NotNil(t, cost)
				require.Zero(t, *cost)
				require.False(t, partial)
			}
		})
	}
}

func TestPelicanGroupCostIncludesFailedAttempts(t *testing.T) {
	plan := groupTestPlan(1)
	plan.ModelID = "public-alias"
	a, b := account(11, "a"), account(12, "b")
	a.Extra = map[string]any{AccountCostMultiplierExtraKey: 0.25}
	b.Extra = map[string]any{AccountCostMultiplierExtraKey: 0.5}
	router := &groupTestRouterFake{steps: []routeStep{{account: a}, {account: b}}}
	svc, _ := newGroupTestService(newGroupTestRepoFake(), router, func(ctx context.Context, id int64, _ string, _ *PelicanTestConfig) (*ScheduledTestResult, error) {
		u := startPelicanTestUsage(ctx, "anthropic")
		u.read(`{"type":"message_start","message":{"model":"claude-sonnet-4","usage":{"input_tokens":1000,"output_tokens":500}}}`)
		u.read(`{"type":"message_stop"}`)
		if id == 11 {
			return &ScheduledTestResult{Status: "failed", ResponseText: "<html>partial", ErrorMessage: "The service is temporarily unavailable. Please retry later."}, nil
		}
		return &ScheduledTestResult{Status: "failed", ResponseText: "not HTML", ErrorMessage: "Model did not return HTML or SVG"}, nil
	})
	svc.billing = newTestBillingService()
	got := svc.runSample(context.Background(), plan, &Group{ID: 4, RateMultiplier: 99})
	require.Equal(t, "failed", got.Status)
	require.Len(t, got.Attempts, 1)
	require.NotNil(t, got.CostUSD)
	// $0.003 input + $0.0075 output, at each account's actual cost multiplier.
	require.InDelta(t, 0.0105*(0.25+0.5), *got.CostUSD, 1e-12)
	require.False(t, got.CostIncomplete)
	require.ElementsMatch(t, []int64{11, 12}, router.released)
}

func TestPelicanGroupCostNoAccountIsFree(t *testing.T) {
	svc, _ := newGroupTestService(newGroupTestRepoFake(), &groupTestRouterFake{}, nil)
	got := svc.runSample(context.Background(), groupTestPlan(1), &Group{ID: 4})
	require.NotNil(t, got.CostUSD)
	require.Zero(t, *got.CostUSD)
	require.False(t, got.CostIncomplete)
}

func TestPelicanGroupCostThroughBackgroundAccountTest(t *testing.T) {
	a := &Account{ID: 11, Name: "test account", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{"api_key": "mock-key", "base_url": "http://chat.example/v1",
			"model_mapping": map[string]any{"public-alias": "claude-sonnet-4"}},
		Extra: map[string]any{AccountCostMultiplierExtraKey: 0.25, openai_compat.ExtraKeyResponsesSupported: false},
	}
	accountSvc, upstream := adaptiveCNAccountTestService(a, &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(
		"data: {\"model\":\"claude-sonnet-4\",\"choices\":[{\"delta\":{\"content\":\"<svg></svg>\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":500}}\n\ndata: [DONE]\n\n")),
	})
	plan := groupTestPlan(1)
	plan.ModelID = "public-alias"
	svc, _ := newGroupTestService(newGroupTestRepoFake(), &groupTestRouterFake{steps: []routeStep{{account: a}}}, accountSvc.RunPelicanBackground)
	svc.billing = newTestBillingService()
	got := svc.runSample(context.Background(), plan, &Group{ID: 4})
	require.Equal(t, "success", got.Status, got.ErrorMessage)
	require.Equal(t, "<svg></svg>", got.ResponseText)
	require.NotNil(t, got.CostUSD)
	require.InDelta(t, 0.002625, *got.CostUSD, 1e-12)
	require.False(t, got.CostIncomplete)
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
	require.Equal(t, "claude-sonnet-4", gjson.GetBytes(upstream.lastBody, "model").String())
}

func TestPelicanTestUsageBufferedStreams(t *testing.T) {
	collector := &pelicanTestUsageCollector{}
	ctx := context.WithValue(context.Background(), pelicanTestUsageKey{}, collector)
	recordPelicanTestSSE(ctx, "openai", "claude-sonnet-4", []byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1000,\"output_tokens\":500}}}\n\n"))
	recordPelicanTestSSE(ctx, "gemini", "claude-sonnet-4", []byte("data:{\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1000,\"candidatesTokenCount\":500}}}\n\n"))
	cost, partial := collector.cost(newTestBillingService(), &Account{Extra: map[string]any{AccountCostMultiplierExtraKey: 1.0}})
	require.NotNil(t, cost)
	require.InDelta(t, 0.021, *cost, 1e-12)
	require.False(t, partial)
}
