//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	compatPingEvent            = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	compatPermissionErrorEvent = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"permission_error\",\"message\":\"illegal request\"},\"status_code\":403}\n\n"
	compatOverloadedErrorEvent = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	compatMessageStartEvent    = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_hold\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":12}}}\n\n"
	compatTextBlockEvents      = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"
	compatFinishEvents = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
)

// 上游先回 HTTP 200 + 若干 ping，再在流体内发 error：首段输出前的 ping 不应写给
// 客户端，否则 handler 判定「已开始输出」而无法切到下一个账号，客户端只拿到
// 心跳和一个错误帧（表现为空响应）。
func TestHandleCCStreamingFromAnthropic_PingsBeforeErrorAreHeldForFailover(t *testing.T) {
	for _, errorEvent := range []string{compatPermissionErrorEvent, compatOverloadedErrorEvent} {
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		stream := compatPingEvent + compatPingEvent + compatPingEvent + errorEvent

		result, err := (&GatewayService{}).handleCCStreamingFromAnthropic(
			&http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))},
			c, "claude-sonnet-4-6", "claude-sonnet-4-6", nil, time.Now(),
		)

		var sseErr *sseStreamErrorEventError
		require.ErrorAs(t, err, &sseErr)
		require.Nil(t, result)
		require.Zero(t, rec.Body.Len(), "held pings must not reach the client before output")
		require.False(t, IsResponseCommitted(c))
	}
}

func TestHandleCCStreamingFromAnthropic_HeldPingsDoNotLeakOnceOutputStarts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	stream := compatPingEvent + compatPingEvent + compatMessageStartEvent + compatTextBlockEvents + compatPingEvent + compatFinishEvents

	result, err := (&GatewayService{}).handleCCStreamingFromAnthropic(
		&http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))},
		c, "claude-sonnet-4-6", "claude-sonnet-4-6", nil, time.Now(),
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.False(t, strings.HasPrefix(body, ": ping"), "pings before the first chunk are held")
	require.Equal(t, 1, strings.Count(body, ": ping\n\n"), "pings after output starts are still forwarded")
	require.Less(t, strings.Index(body, `"content":"hello"`), strings.Index(body, ": ping\n\n"))
	require.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"))
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
}

// 保留窗口过后仍无输出时恢复转发心跳，保持 Wei-Shaw/sub2api#7854 对空闲超时的保护。
func TestHandleCCStreamingFromAnthropic_PingForwardedAfterHoldWindow(t *testing.T) {
	hold := 50 * time.Millisecond
	writer := &ccPingResponseWriter{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan string, 8)}
	c, _ := gin.CreateTestContext(writer)
	c.Set(ccPingHoldContextKey, hold)
	reader, upstream := io.Pipe()
	finished := make(chan error, 1)
	go func() {
		_, err := (&GatewayService{}).handleCCStreamingFromAnthropic(
			&http.Response{Header: http.Header{}, Body: reader}, c, "claude-sonnet-4-6", "claude-sonnet-4-6", nil, time.Now(),
		)
		finished <- err
	}()
	t.Cleanup(func() {
		_ = upstream.Close()
		_ = reader.Close()
	})

	_, err := io.WriteString(upstream, compatPingEvent)
	require.NoError(t, err)
	select {
	case body := <-writer.flushed:
		t.Fatalf("ping inside the hold window was flushed: %q", body)
	case <-time.After(2 * hold):
	}

	_, err = io.WriteString(upstream, compatPingEvent)
	require.NoError(t, err)
	select {
	case body := <-writer.flushed:
		require.Equal(t, ": ping\n\n", body)
	case <-time.After(5 * time.Second):
		t.Fatal("ping after the hold window was not flushed")
	}

	require.NoError(t, upstream.Close())
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not finish after EOF")
	}
}

func newCompatFailoverTestRequest(t *testing.T, endpoint string, stream bool, upstreamBody string) (*GatewayService, *gin.Context, *httptest.ResponseRecorder, []byte, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	streamFlag := "false"
	if stream {
		streamFlag = "true"
	}
	body := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":` + streamFlag + `}`
	path := "/v1/chat/completions"
	if endpoint == "responses" {
		body = `{"model":"claude-sonnet-4-6","input":"hi","stream":` + streamFlag + `}`
		path = "/v1/responses"
	}
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstreamBody))}
	svc := &GatewayService{
		cfg:              rawChatCompletionsTestConfig(),
		httpUpstream:     &anthropicHTTPUpstreamRecorder{resp: resp},
		rateLimitService: &RateLimitService{accountRepo: &errorPolicyRepoStub{}, cfg: &config.Config{}},
	}
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test"}}
	return svc, c, rec, []byte(body), account
}

func forwardCompatForTest(svc *GatewayService, c *gin.Context, endpoint string, body []byte, account *Account) (*ForwardResult, error) {
	if endpoint == "responses" {
		return svc.ForwardAsResponses(context.Background(), c, account, body, nil)
	}
	return svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
}

func TestForwardAsChatCompletions_StreamPingsThenErrorFailsOverWithoutOutput(t *testing.T) {
	for _, tc := range []struct {
		name       string
		errorEvent string
		wantStatus int
	}{
		{name: "permission_error", errorEvent: compatPermissionErrorEvent, wantStatus: http.StatusForbidden},
		{name: "overloaded_error", errorEvent: compatOverloadedErrorEvent, wantStatus: 529},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, c, rec, body, account := newCompatFailoverTestRequest(t, "chat_completions", true, compatPingEvent+compatPingEvent+tc.errorEvent)

			result, err := forwardCompatForTest(svc, c, "chat_completions", body, account)

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, tc.wantStatus, failoverErr.StatusCode)
			require.True(t, failoverErr.ShouldRetryNextAccount())
			require.Nil(t, result)
			require.Zero(t, rec.Body.Len(), "nothing may reach the client so the handler can switch accounts")
		})
	}
}

// 非流式（缓冲）路径在流体内收到非安全类 error 事件时，客户端还没有收到任何
// 字节：应转为故障转移，而不是忽略事件后返回 502 或截断的半截响应。
func TestForwardAsCompatBuffered_UpstreamErrorEventFailsOver(t *testing.T) {
	for _, endpoint := range []string{"chat_completions", "responses"} {
		for _, tc := range []struct {
			name       string
			stream     string
			wantStatus int
		}{
			{name: "permission_before_message_start", stream: compatPingEvent + compatPermissionErrorEvent, wantStatus: http.StatusForbidden},
			{name: "overloaded_before_message_start", stream: compatOverloadedErrorEvent, wantStatus: 529},
			{name: "permission_after_partial_output", stream: compatMessageStartEvent + compatTextBlockEvents + compatPermissionErrorEvent + compatFinishEvents, wantStatus: http.StatusForbidden},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				svc, c, rec, body, account := newCompatFailoverTestRequest(t, endpoint, false, tc.stream)

				result, err := forwardCompatForTest(svc, c, endpoint, body, account)

				var failoverErr *UpstreamFailoverError
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, tc.wantStatus, failoverErr.StatusCode)
				require.True(t, failoverErr.ShouldRetryNextAccount())
				require.Nil(t, result, "a failed-over attempt is not billed")
				require.Zero(t, rec.Body.Len())
				require.False(t, IsResponseCommitted(c))
			})
		}
	}
}
