package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCopilotSDKAccountGate(t *testing.T) {
	for _, tc := range []struct {
		platform, kind string
		flag           any
		want           bool
	}{
		{PlatformOpenAI, AccountTypeAPIKey, true, true},
		{PlatformOpenAI, AccountTypeOAuth, true, false},
		{PlatformAnthropic, AccountTypeAPIKey, true, false},
		{PlatformOpenAI, AccountTypeAPIKey, "true", false},
	} {
		a := &Account{Platform: tc.platform, Type: tc.kind, Extra: map[string]any{"openai_copilot_sdk": tc.flag}}
		require.Equal(t, tc.want, a.IsCopilotSDKEnabled())
		if tc.want {
			require.True(t, a.IsOpenAIPassthroughEnabled())
			require.False(t, a.IsOpenAIResponsesWebSocketV2Enabled())
			require.True(t, a.IsOpenAIWSForceHTTPEnabled())
			require.Equal(t, OpenAIWSIngressModeOff, a.ResolveOpenAIResponsesWebSocketV2Mode(OpenAIWSIngressModePassthrough))
			require.False(t, shouldForwardOpenAIResponsesViaChatCompletions(a, []byte(`{}`)))
		}
	}
	var absent *Account
	require.False(t, absent.IsCopilotSDKEnabled())
}

func TestCopilotSDKConflictDoesNotFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"auto","input":"test"}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 409, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Pending turn unavailable"}}`))}}
	svc := openAIClientToolsTestService(upstream)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
	a := &Account{ID: 71, Status: StatusActive, Schedulable: true, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sidecar-key"}, Extra: map[string]any{"openai_copilot_sdk": true}}
	_, err := svc.Forward(c.Request.Context(), c, a, body)
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.NotErrorAs(t, err, &failover)
	require.Equal(t, 409, recorder.Code)
	require.Len(t, upstream.requests, 1)
}

func TestCopilotSDKCancellationIsNotDetached(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_copilot_sdk": true}}
	sdkCtx, release := openAIPassthroughContext(ctx, a)
	defer release()
	ordinaryCtx, releaseOrdinary := openAIPassthroughContext(ctx, &Account{})
	defer releaseOrdinary()
	cancel()
	require.ErrorIs(t, sdkCtx.Err(), context.Canceled)
	require.NoError(t, ordinaryCtx.Err())
}

func TestCopilotSDKForwardPreservesNativeContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		t.Run(path, func(t *testing.T) {
			body := []byte(`{"model":"auto","stream":false,"store":false,"session_id":"thread-A","client_metadata":{"session_id":"parent","thread_id":"child"},"tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}],"input":[{"type":"additional_tools","id":"at_1","tools":[{"type":"namespace","name":"fs","tools":[{"type":"function","name":"read"}]}]},{"type":"reasoning","id":"opaque-id","encrypted_content":"opaque"},{"type":"custom_tool_call","id":"sdk-id","call_id":"ghcpsdk_opaque","namespace":"functions","name":"apply_patch","input":"patch"},{"type":"custom_tool_call_output","call_id":"ghcpsdk_opaque","output":"ok"}]}`)
			response := `{"id":"resp_sdk","status":"completed","output":[{"type":"custom_tool_call","call_id":"ghcpsdk_opaque","name":"apply_patch","input":"patch"}],"usage":{"input_tokens":3,"output_tokens":1}}`
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}}
			svc := openAIClientToolsTestService(upstream)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest("POST", path, bytes.NewReader(body))
			c.Request.Header.Set("X-Sub2API-Client-ID", "spoofed")
			c.Set("api_key", &APIKey{ID: 42, AllowLongContext: true})
			account := &Account{ID: 71, Status: StatusActive, Schedulable: true, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sidecar-key"}, Extra: map[string]any{"openai_copilot_sdk": true}}
			result, err := svc.Forward(c.Request.Context(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.JSONEq(t, string(body), string(upstream.lastBody))
			require.Equal(t, path, upstream.lastReq.URL.Path)
			require.Equal(t, "Bearer sidecar-key", upstream.lastReq.Header.Get("Authorization"))
			require.Equal(t, "42", upstream.lastReq.Header.Get("X-Sub2API-Client-ID"))
			require.Contains(t, recorder.Body.String(), `"custom_tool_call"`)
		})
	}
}

func TestCopilotSDKStreamingPreservesCustomCallAndUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"auto","stream":true,"tools":[{"type":"custom","name":"apply_patch"}],"input":"fix"}`)
	sse := "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"custom_tool_call\",\"call_id\":\"s2cp1_signed\",\"name\":\"apply_patch\",\"input\":\"patch\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_sdk\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":9,\"output_tokens\":2}}}\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}}
	svc := openAIClientToolsTestService(upstream)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	a := &Account{ID: 71, Status: StatusActive, Schedulable: true, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sidecar-key"}, Extra: map[string]any{"openai_copilot_sdk": true}}
	result, err := svc.Forward(ctx, c, a, body)
	require.NoError(t, err)
	require.Equal(t, 9, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Contains(t, recorder.Body.String(), `"call_id":"s2cp1_signed"`)
	require.Contains(t, recorder.Body.String(), `"type":"custom_tool_call"`)
	cancel()
	require.ErrorIs(t, upstream.lastReq.Context().Err(), context.Canceled)
}

func TestCopilotSDKReadStopsOnCancelledClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r, w := io.Pipe()
	defer func() { _ = w.Close() }()
	defer func() { _ = r.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_copilot_sdk": true}}
	svc := openAIClientToolsTestService(nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.handleStreamingResponsePassthrough(ctx, &http.Response{StatusCode: 200, Header: http.Header{}, Body: r}, c, a, time.Now(), "auto", "auto")
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SDK stream continued draining after the caller cancelled")
	}
}
