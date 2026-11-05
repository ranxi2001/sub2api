//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIUsageProtectionRequiresExplicitValidCacheCounter(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, usage string
		known       bool
		tokens      int
	}{
		{"missing", `{"input_tokens":2000}`, false, 0},
		{"zero", `{"input_tokens":2000,"input_tokens_details":{"cached_tokens":0}}`, true, 0},
		{"positive", `{"input_tokens":2000,"prompt_tokens_details":{"cached_tokens":1280}}`, true, 1280},
		{"null", `{"input_tokens":2000,"input_tokens_details":{"cached_tokens":null}}`, false, 0},
		{"string", `{"input_tokens":2000,"input_tokens_details":{"cached_tokens":"0"}}`, false, 0},
		{"negative", `{"input_tokens":2000,"input_tokens_details":{"cached_tokens":-1}}`, false, 0},
		{"fractional", `{"input_tokens":2000,"input_tokens_details":{"cached_tokens":0.5}}`, false, 0},
		{"canonical zero wins", `{"input_tokens":2000,"input_tokens_details":{"cached_tokens":0},"cache_read_input_tokens":1280}`, true, 0},
		{"legacy explicit zero", `{"input_tokens":2000,"cache_read_input_tokens":0}`, true, 0},
		{"conflicting aliases", `{"input_tokens":2000,"cache_read_input_tokens":0,"cached_tokens":1280}`, false, 1280},
		{"legacy matching aliases", `{"input_tokens":2000,"cache_read_input_tokens":0,"cached_tokens":0}`, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"usage":` + tc.usage + `}`))
			require.True(t, ok)
			require.Equal(t, tc.known, usage.CacheReadKnown)
			require.Equal(t, tc.tokens, usage.CacheReadInputTokens)
		})
	}
}

func TestOpenAIUsageProtectionOnlyUsesSuccessfulTerminalEvidence(t *testing.T) {
	t.Parallel()
	svc := &OpenAIGatewayService{}
	for _, tc := range []struct {
		name, event, payload string
		known                bool
	}{
		{"completed zero", "response.completed", `{"response":{"usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":0}}}}`, true},
		{"completed missing cache", "response.completed", `{"response":{"usage":{"input_tokens":2000}}}`, false},
		{"terminal missing usage", "response.completed", `{"response":{"id":"resp_1"}}`, false},
		{"failed", "response.failed", `{"response":{"usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":0}}}}`, false},
		{"incomplete", "response.incomplete", `{"response":{"usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":0}}}}`, false},
		{"progressive", "response.in_progress", `{"type":"response.in_progress","usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":0}}}`, false},
		{"conflicting status", "response.completed", `{"response":{"status":"incomplete","usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":0}}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := OpenAIUsage{InputTokens: 2000, CacheReadInputTokens: 500, CacheReadKnown: true}
			svc.parseSSEUsageBytesWithType([]byte(tc.payload), tc.event, &usage)
			require.Equal(t, tc.known, usage.CacheReadKnown)
		})
	}
}

func TestOpenAIUsageProtectionMetadataDoesNotLeakToClients(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(OpenAIUsage{CacheReadKnown: true, InputTokens: 2000})
	require.NoError(t, err)
	require.NotContains(t, string(data), "Known")
	require.NotContains(t, string(data), "known")
}

func TestOpenAIUsageProtectionExcludesPartialAndNonTextRequests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*OpenAIForwardResult)
	}{
		{"unknown cache", func(r *OpenAIForwardResult) { r.Usage.CacheReadKnown = false }},
		{"missing usage", func(r *OpenAIForwardResult) { r.UsageUnavailable = true }},
		{"disconnect", func(r *OpenAIForwardResult) { r.ClientDisconnect = true }},
		{"read interrupted", func(r *OpenAIForwardResult) { r.streamReadIncomplete = true }},
		{"failed websocket", func(r *OpenAIForwardResult) { r.OpenAIWSMode = true; r.UpstreamTerminalEvent = "response.failed" }},
		{"image", func(r *OpenAIForwardResult) { r.ImageCount = 1 }},
		{"image input", func(r *OpenAIForwardResult) { r.Usage.ImageInputTokens = 1 }},
		{"video", func(r *OpenAIForwardResult) { r.VideoCount = 1 }},
		{"zero input", func(r *OpenAIForwardResult) { r.Usage.InputTokens = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &OpenAIForwardResult{Usage: OpenAIUsage{InputTokens: 2000, CacheReadKnown: true}}
			require.True(t, openAIGroupAntiDistillationUsageEligible(r))
			tc.change(r)
			require.False(t, openAIGroupAntiDistillationUsageEligible(r))
		})
	}
}

func TestOpenAIUsageProtectionTypedConversionDoesNotMakeImageRequestsText(t *testing.T) {
	t.Parallel()
	for _, details := range []string{
		`"input_tokens_details":{"cached_tokens":0,"image_tokens":100}`,
		`"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"image_tokens":100}`,
	} {
		raw := []byte(`{"input_tokens":2000,"output_tokens":100,` + details + `}`)
		observed, ok := extractOpenAIUsageFromJSONBytes(append(append([]byte(`{"usage":`), raw...), '}'))
		require.True(t, ok)
		require.True(t, observed.CacheReadKnown)
		var typed apicompat.ResponsesUsage
		require.NoError(t, json.Unmarshal(raw, &typed))
		converted := copyOpenAIUsageFromResponsesUsage(&typed, observed)
		require.False(t, converted.CacheReadKnown, "typed conversion must not turn known image usage into a text protection sample")
		require.False(t, openAIGroupAntiDistillationUsageEligible(&OpenAIForwardResult{Usage: converted}))
	}
}

func TestOpenAIUsageProtectionCCFallbackRequiresCompletedGeneration(t *testing.T) {
	for _, protocol := range []string{"responses", "messages"} {
		for _, tc := range []struct {
			name, delta, finish, suffix string
			known                       bool
		}{
			{"completed", `"content":"ok"`, "stop", "data: [DONE]\n\n", true},
			{"missing upstream terminal", `"content":"ok"`, "stop", "", false},
			{"reasoning failure", `"reasoning_content":"unfinished"`, "length", "data: [DONE]\n\n", false},
		} {
			if protocol == "messages" && tc.name == "reasoning failure" {
				continue
			}
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				body := `data: {"choices":[{"index":0,"delta":{` + tc.delta + `},"finish_reason":"` + tc.finish + `"}]}` + "\n\n" +
					`data: {"choices":[],"usage":{"prompt_tokens":2000,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":0}}}` + "\n\n" + tc.suffix
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				var result *OpenAIForwardResult
				var err error
				if protocol == "responses" {
					result, err = svc.streamChatCompletionsAsResponses(c, resp, forceChatResponsesFallbackAccount(), "gpt-5.4", nil, nil, false, nil, "gpt-5.4", "gpt-5.4", nil, nil, false, time.Now(), nil)
				} else {
					result, err = svc.streamChatCompletionsAsAnthropic(c, resp, forceChatResponsesFallbackAccount(), "gpt-5.4", "gpt-5.4", "gpt-5.4", nil, nil, time.Now())
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, tc.known, result.Usage.CacheReadKnown)
			})
		}
	}
}

func TestOpenAIUsageProtectionNativeSSETerminalCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, eventLine, terminal string }{
		{"legacy done payload", "", `{"type":"response.done","response":{"status":"completed","usage":{"input_tokens":2000,"output_tokens":20,"input_tokens_details":{"cached_tokens":0}}}}`},
		{"completed event field", "event: response.completed\n", `{"response":{"status":"completed","usage":{"input_tokens":2000,"output_tokens":20,"input_tokens_details":{"cached_tokens":0}}}}`},
		{"done event field", "event: response.done\n", `{"response":{"status":"completed","usage":{"input_tokens":2000,"output_tokens":20,"input_tokens_details":{"cached_tokens":0}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			body := "data: " + `{"type":"response.output_text.delta","delta":"ok"}` + "\n\n" + tc.eventLine + "data: " + tc.terminal + "\n\n"
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, time.Now(), "gpt-5.4", "gpt-5.4")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.usage)
			require.True(t, result.usage.CacheReadKnown, "a successful terminal must retain explicit cache statistics without needing a DONE sentinel")
			require.True(t, openAIGroupAntiDistillationUsageEligible(&OpenAIForwardResult{Usage: *result.usage}))
		})
	}
}

func TestOpenAIUsageProtectionNativeSSERetainsProgressiveMediaEvidence(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	terminal := `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2000,"output_tokens":20,"input_tokens_details":{"cached_tokens":0}}}}`
	body := "data: " + `{"type":"response.in_progress","usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":0,"image_tokens":100}}}` + "\n\n" +
		"data: " + `{"type":"response.output_text.delta","delta":"ok"}` + "\n\n" + "data: " + terminal + "\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
	result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, time.Now(), "gpt-5.4", "gpt-5.4")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.usage.cacheReadMediaObserved)
	require.False(t, result.usage.CacheReadKnown)
	require.Zero(t, result.usage.ImageInputTokens, "protection must not alter terminal billing token counters")
	require.False(t, openAIGroupAntiDistillationUsageEligible(&OpenAIForwardResult{Usage: *result.usage}))
	// Buffered conversion may read repeated terminal frames. They must not erase
	// media evidence retained from an earlier progressive frame either.
	buffered := svc.parseSSEUsageFromBody(body + "data: " + terminal + "\n\n")
	require.True(t, buffered.cacheReadMediaObserved)
	require.False(t, buffered.CacheReadKnown)
	var typed apicompat.ResponsesStreamEvent
	require.NoError(t, json.Unmarshal([]byte(terminal), &typed))
	converted := copyOpenAIUsageFromResponsesUsage(typed.Response.Usage, *buffered)
	require.True(t, converted.cacheReadMediaObserved)
	require.False(t, openAIGroupAntiDistillationUsageEligible(&OpenAIForwardResult{Usage: converted}))
}

func TestOpenAIUsageProtectionWSPoolRetainsProgressiveMediaEvidence(t *testing.T) {
	usage := &OpenAIUsage{}
	parseOpenAIWSResponseUsageFromCompletedEvent([]byte(`{"type":"response.in_progress","usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":0,"image_tokens":100}}}`), usage)
	parseOpenAIWSResponseUsageFromCompletedEvent([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":2000,"output_tokens":20,"input_tokens_details":{"cached_tokens":0}}}}`), usage)
	require.True(t, usage.cacheReadMediaObserved)
	require.False(t, usage.CacheReadKnown)
	require.Zero(t, usage.ImageInputTokens)
}
