//go:build unit

package service

import (
	"context"
	"errors"
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

// Writes always succeed. Only the transport flush fails, after the selected
// bytes have entered its buffer; Gin's void Flush would hide this failure.
type issue985FlushWriter struct {
	*httptest.ResponseRecorder
	marker                                                             string
	failed                                                             bool
	writesAfterFailure, flushesAfterFailure, headerChangesAfterFailure int
}

func (w *issue985FlushWriter) Write(p []byte) (int, error) {
	if w.failed {
		w.writesAfterFailure++
	}
	return w.ResponseRecorder.Write(p)
}
func (w *issue985FlushWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *issue985FlushWriter) WriteHeader(code int) {
	if w.failed {
		w.headerChangesAfterFailure++
	}
	w.ResponseRecorder.WriteHeader(code)
}
func (w *issue985FlushWriter) FlushError() error {
	if w.failed {
		w.flushesAfterFailure++
	}
	if strings.Contains(w.Body.String(), w.marker) {
		w.failed = true
		return io.ErrClosedPipe
	}
	w.ResponseRecorder.Flush()
	return nil
}
func (w *issue985FlushWriter) Flush() { _ = w.FlushError() }

// Model middleware whose FlushError is a lifecycle boundary. The helper must
// call it rather than bypass it while searching for the underlying transport.
type issue985FlushGuard struct {
	gin.ResponseWriter
	calls int
}

func (w *issue985FlushGuard) FlushError() error {
	w.calls++
	return FlushGatewayResponse(w.ResponseWriter)
}

func TestIssue985FlushFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"chat", "responses", "native_chat", "native_responses", "native_messages", "passthrough"} {
		for _, phase := range []string{"intermediate", "final"} {
			t.Run(route+"/"+phase, func(t *testing.T) {
				marker := "fixture text"
				if phase == "final" {
					marker = "[DONE]"
					if strings.Contains(route, "responses") {
						marker = "response.completed"
					}
					if route == "native_messages" || route == "passthrough" {
						marker = "message_stop"
					}
				}
				writer := &issue985FlushWriter{ResponseRecorder: httptest.NewRecorder(), marker: marker}
				c, _ := gin.CreateTestContext(writer)
				guard := &issue985FlushGuard{ResponseWriter: c.Writer}
				c.Writer = guard
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				wire := strings.Replace(issue985Stream(), `"input_tokens":17`, `"input_tokens":17,"cache_read_input_tokens":7,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_1h_input_tokens":5}`, 1)
				resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
				var err error
				var usage ClaudeUsage
				var disconnected bool
				svc := &GatewayService{}
				native := &OpenAIGatewayService{}
				if strings.HasPrefix(route, "native_") {
					var result *OpenAIForwardResult
					switch route {
					case "native_chat":
						result, err = native.handleCCStreamingFromNativeAnthropic(resp, c, "fixture", "fixture", "fixture", nil, time.Now())
					case "native_responses":
						result, err = native.handleResponsesStreamingFromNativeAnthropic(resp, c, "fixture", "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
					default:
						result, err = native.handleNativeAnthropicStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, "fixture", "fixture", "fixture", nil, time.Now())
					}
					require.NotNil(t, result)
					require.False(t, result.UsageUnavailable)
					u := result.Usage
					usage = ClaudeUsage{InputTokens: u.InputTokens - u.CacheReadInputTokens - u.CacheCreationInputTokens, OutputTokens: u.OutputTokens, CacheReadInputTokens: u.CacheReadInputTokens, CacheCreationInputTokens: u.CacheCreationInputTokens, CacheCreation1hTokens: u.CacheCreation1hTokens}
					disconnected = result.ClientDisconnect
				} else if route == "passthrough" {
					result, forwardErr := svc.handleStreamingResponseAnthropicAPIKeyPassthrough(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "fixture")
					err = forwardErr
					require.NotNil(t, result)
					usage, disconnected = *result.usage, result.clientDisconnect
				} else {
					var result *ForwardResult
					if route == "chat" {
						result, err = svc.handleCCStreamingFromAnthropic(resp, c, "fixture", "fixture", nil, time.Now())
					} else {
						result, err = svc.handleResponsesStreamingResponse(resp, c, "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
					}
					require.NotNil(t, result)
					usage, disconnected = result.Usage, result.ClientDisconnect
				}
				require.ErrorIs(t, err, context.Canceled)
				var replay *UpstreamFailoverError
				var semanticReplay *sseStreamErrorEventError
				require.False(t, errors.As(err, &replay))
				require.False(t, errors.As(err, &semanticReplay))
				require.True(t, disconnected)
				require.True(t, writer.failed, "must reach a successful Write followed by FlushError")
				require.Positive(t, guard.calls, "middleware lifecycle boundary must be preserved")
				require.Equal(t, 17, usage.InputTokens)
				require.Equal(t, 3, usage.OutputTokens)
				require.Equal(t, 7, usage.CacheReadInputTokens)
				require.Equal(t, 5, usage.CacheCreationInputTokens)
				require.Equal(t, 5, usage.CacheCreation1hTokens)
				require.Zero(t, writer.writesAfterFailure)
				require.Zero(t, writer.flushesAfterFailure)
				require.Zero(t, writer.headerChangesAfterFailure)
				require.Equal(t, http.StatusOK, writer.Code)
				if phase == "intermediate" {
					require.NotContains(t, writer.Body.String(), "[DONE]")
					require.NotContains(t, writer.Body.String(), "response.completed")
					require.NotContains(t, writer.Body.String(), `"finish_reason":"stop"`)
					require.NotContains(t, writer.Body.String(), "message_stop")
				}
			})
		}
	}
}
