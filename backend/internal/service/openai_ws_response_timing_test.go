package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type wsResponseTimingFlushFailure struct{ gin.ResponseWriter }

func (w *wsResponseTimingFlushFailure) FlushError() error { return io.ErrClosedPipe }

type wsResponseTimingShortWrite struct{ gin.ResponseWriter }

func (w *wsResponseTimingShortWrite) Write(p []byte) (int, error) {
	return w.ResponseWriter.Write(p[:len(p)-1])
}

func TestOpenAIWSResponseTimingSeparatesMetadataReasoningAndText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		collector := requesttiming.New(start, 0)
		ctx := requesttiming.With(context.Background(), collector)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
		SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
		conn := &openAIWSCaptureConn{readDelays: []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond}, events: [][]byte{
			[]byte(`{"type":"response.created","response":{"id":"resp_timing"}}`),
			[]byte(`{"type":"response.reasoning_text.delta","delta":"synthetic"}`),
			[]byte(`{"type":"response.output_text.delta","delta":"OK"}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_timing","usage":{"input_tokens":3,"output_tokens":2}}}`),
		}}
		svc := wsSSETestService(t, &wsSSETestDialer{conn: conn}, &httpUpstreamRecorder{})
		result, err := svc.Forward(ctx, c, wsSSETestAccount(), []byte(wsSSETestRequest))
		require.NoError(t, err)
		require.NotNil(t, result.FirstTokenMs)
		require.Equal(t, 300, *result.FirstTokenMs, "legacy WS semantic TTFT keeps its meaning")
		collector.Finish(http.StatusOK, false)
		collector.WhenFinished(func(s requesttiming.Snapshot) {
			require.Equal(t, 1, s.FinalResponseAttempt)
			require.Len(t, s.Attempts, 1, "handshake and frame writes remain routes")
			a := s.Attempts[0]
			require.Equal(t, "websocket", a.Route.Upstream)
			require.Equal(t, "ctx_pool", a.Route.Mode)
			require.Equal(t, openAIOAuthWSSSEAccelerationReason, a.Route.Reason)
			require.Equal(t, "completed", a.Terminal)
			require.NotNil(t, a.EndMS)
			v := requesttiming.DeriveContentObservation(s)
			require.Equal(t, "native_websocket_events", v.Source)
			require.Equal(t, 100.0, *v.SSEEventFirstMS)
			require.Equal(t, 300.0, *v.SemanticFirstMS)
			require.Equal(t, 300.0, *v.ReasoningFirstMS)
			require.Equal(t, 800.0, *v.VisibleTextFirstMS)
			require.Nil(t, v.ResponseByteFirstMS)
		})
	})
}

func TestOpenAIWSResponseTimingMetadataBeforeEOFDoesNotInventContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		collector := requesttiming.New(start, 0)
		ctx := requesttiming.With(context.Background(), collector)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
		SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
		conn := &openAIWSCaptureConn{readDelays: []time.Duration{100 * time.Millisecond}, events: [][]byte{
			[]byte(`{"type":"response.created","response":{"id":"resp_metadata_eof"}}`),
		}}
		upstream := &httpUpstreamRecorder{}
		svc := wsSSETestService(t, &wsSSETestDialer{conn: conn}, upstream)
		_, err := svc.Forward(ctx, c, wsSSETestAccount(), []byte(wsSSETestRequest))
		require.Error(t, err)
		require.Nil(t, upstream.lastReq, "already sent WS requests must not replay via HTTP")
		collector.Finish(http.StatusOK, false)
		collector.WhenFinished(func(s requesttiming.Snapshot) {
			require.Len(t, s.Attempts, 1)
			require.NotNil(t, s.Attempts[0].EndMS)
			require.NotEmpty(t, s.Attempts[0].Error)
			v := requesttiming.DeriveContentObservation(s)
			require.Equal(t, 100.0, *v.SSEEventFirstMS)
			require.Nil(t, v.SemanticFirstMS)
			require.Nil(t, v.ReasoningFirstMS)
			require.Nil(t, v.VisibleTextFirstMS)
			require.Nil(t, v.ResponseByteFirstMS)
		})
	})
}

func TestOpenAIWSResponseTimingFailedDeliveryDoesNotRecordFlush(t *testing.T) {
	for _, failure := range []string{"flush", "short_write"} {
		t.Run(failure, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			collector := requesttiming.New(time.Now(), 0)
			ctx := requesttiming.With(context.Background(), collector)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			if failure == "flush" {
				c.Writer = &wsResponseTimingFlushFailure{ResponseWriter: c.Writer}
			} else {
				c.Writer = &wsResponseTimingShortWrite{ResponseWriter: c.Writer}
			}
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			conn := &openAIWSCaptureConn{events: [][]byte{
				[]byte(`{"type":"response.created","response":{"id":"resp_failed_delivery"}}`),
				[]byte(`{"type":"response.output_text.delta","delta":"OK"}`),
				[]byte(`{"type":"response.completed","response":{"id":"resp_failed_delivery","usage":{"input_tokens":3,"output_tokens":2}}}`),
			}}
			svc := wsSSETestService(t, &wsSSETestDialer{conn: conn}, &httpUpstreamRecorder{})
			result, err := svc.Forward(ctx, c, wsSSETestAccount(), []byte(wsSSETestRequest))
			require.NoError(t, err, "upstream completion still preserves usage after disconnect")
			require.True(t, result.ClientDisconnect)
			require.Equal(t, 3, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
			collector.Finish(http.StatusOK, false)
			collector.WhenFinished(func(s requesttiming.Snapshot) {
				require.NotContains(t, s.Events, "first_sse_event_flush")
				require.Nil(t, requesttiming.DeriveContentObservation(s).SSEEventFirstMS)
			})
		})
	}
}
