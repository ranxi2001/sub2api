//go:build unit && issue1085

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	testutil "github.com/Wei-Shaw/sub2api/internal/testutil/sse1085"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIssue1085TerminalDelivery(t *testing.T) {
	for _, reader := range []string{"native_async", "passthrough"} {
		for _, failure := range []string{"success", "write", "flush"} {
			t.Run(reader+"/"+failure, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				body := testutil.NewIssue1085Body(
					"data: {\"type\":\"response.output_text.delta\",\"delta\":\"fixture delta\"}\n\n",
					"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"codex_session_failover_context_required\",\"message\":\"fixture context\"},\"usage\":{\"input_tokens\":7,\"output_tokens\":2}}}\n\n" +
						"data: [DONE]\n\n")
				defer body.Close()
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				writer := &testutil.Issue1085Writer{ResponseWriter: c.Writer, Body: body, Context: ctx, Native: reader == "native_async", Failure: failure}
				c.Writer = writer
				svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 30}}, toolCorrector: NewCodexToolCorrector()}
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}
				account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				var usage *OpenAIUsage
				var disconnected bool
				var err error
				if reader == "native_async" {
					var result *openaiStreamingResult
					result, err = svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "", "")
					require.NotNil(t, result)
					usage, disconnected = result.usage, result.clientDisconnect
				} else {
					var result *openaiStreamingResultPassthrough
					result, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, account, time.Now(), "", "")
					require.NotNil(t, result)
					usage, disconnected = result.usage, result.clientDisconnect
				}
				require.ErrorContains(t, err, "upstream response failed")
				var replay *UpstreamFailoverError
				require.NotErrorAs(t, err, &replay)
				require.True(t, writer.DeltaFlushed)
				require.True(t, writer.TerminalWrite)
				require.NoError(t, writer.QueueWaitError)
				if reader == "native_async" {
					require.True(t, writer.QueueFilled, "must exercise non-drained terminal boundary")
				}
				if failure == "flush" {
					require.True(t, writer.TerminalFlush)
				}
				require.Equal(t, failure != "success", disconnected)
				require.Equal(t, failure == "success", HasOpsUpstreamSSETerminal(c), "ISSUE1085_DELIVERY_EVIDENCE")
				require.Equal(t, 7, usage.InputTokens)
				require.Equal(t, 2, usage.OutputTokens)
			})
		}
	}
}
