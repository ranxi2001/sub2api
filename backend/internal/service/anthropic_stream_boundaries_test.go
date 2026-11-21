//go:build unit

package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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

func TestIssue985AdapterBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	canonical := issue985Stream()
	stop := issue985Frame("message_stop", `{"type":"message_stop"}`)
	var dataOnly, reordered strings.Builder
	for _, frame := range strings.Split(strings.TrimSpace(canonical), "\n\n") {
		lines := strings.Split(frame, "\n")
		_, _ = dataOnly.WriteString(lines[1] + "\n\n")
		_, _ = reordered.WriteString(lines[1] + "\nid: fixture\n" + lines[0] + "\n\n")
	}
	missing := strings.ReplaceAll(strings.ReplaceAll(canonical, `,"usage":{"input_tokens":17,"output_tokens":0}`, ""), `,"usage":{"output_tokens":3}`, "")
	for _, tc := range []struct {
		name, wire    string
		fail          bool
		input, output int
	}{
		{"crlf", strings.ReplaceAll(canonical, "\n", "\r\n"), false, 17, 3},
		{"data_only", dataOnly.String(), false, 17, 3},
		{"field_order", reordered.String(), false, 17, 3},
		{"event_without_data", "event: message_stop\n\n" + canonical, false, 17, 3},
		{"cr", strings.ReplaceAll(canonical, "\n", "\r"), false, 17, 3},
		{"bom", "\xef\xbb\xbf" + canonical, false, 17, 3},
		{"compact", strings.ReplaceAll(strings.ReplaceAll(canonical, "event: ", "event:"), "data: ", "data:"), false, 17, 3},
		{"eof_frame", strings.TrimRight(canonical, "\n"), false, 17, 3},
		{"unknown", issue985Frame("future_event", `{"type":"future_event","opaque":true}`) + canonical, false, 17, 3},
		{"zero", strings.ReplaceAll(strings.ReplaceAll(canonical, `"input_tokens":17`, `"input_tokens":0`), `"output_tokens":3`, `"output_tokens":0`), false, 0, 0},
		{"missing", missing, true, 0, 0},
		{"field_missing", strings.ReplaceAll(canonical, `"input_tokens":17,`, ""), true, 0, 3},
		{"stop_reason_only", strings.TrimSuffix(canonical, stop), true, 17, 3},
		{"terminal_error", canonical + issue985Frame("error", `{"type":"error","error":{"type":"overloaded_error","message":"synthetic"}}`), true, 17, 3},
		{"malformed", strings.TrimSuffix(canonical, stop) + issue985Frame("message_stop", `{`), true, 17, 3},
		{"mismatch", strings.ReplaceAll(canonical, "event: message_stop", "event: ping"), true, 17, 3},
	} {
		for _, adapter := range []string{"chat", "responses", "native_chat", "native_responses"} {
			for _, stream := range []bool{true, false} {
				mode := "buffered"
				if stream {
					mode = "stream"
				}
				t.Run(adapter+"/"+mode+"/"+tc.name, func(t *testing.T) {
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
					resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.wire))}
					var err error
					var input, output int
					if strings.HasPrefix(adapter, "native") {
						svc := &OpenAIGatewayService{}
						var result *OpenAIForwardResult
						if adapter == "native_chat" {
							if stream {
								result, err = svc.handleCCStreamingFromNativeAnthropic(resp, c, "client-model", "fixture-model", "fixture-model", nil, time.Now())
							} else {
								result, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, "client-model", "fixture-model", "fixture-model", nil, time.Now())
							}
						} else {
							if stream {
								result, err = svc.handleResponsesStreamingFromNativeAnthropic(resp, c, "client-model", "fixture-model", "fixture-model", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
							} else {
								result, err = svc.handleResponsesBufferedFromNativeAnthropic(resp, c, "client-model", "fixture-model", "fixture-model", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
							}
						}
						if result != nil {
							input, output = result.Usage.InputTokens, result.Usage.OutputTokens
						}
					} else {
						svc := &GatewayService{}
						var result *ForwardResult
						if adapter == "chat" {
							if stream {
								result, err = svc.handleCCStreamingFromAnthropic(resp, c, "client-model", "fixture-model", nil, time.Now())
							} else {
								result, err = svc.handleCCBufferedFromAnthropic(resp, c, "client-model", "fixture-model", nil, time.Now())
							}
						} else {
							if stream {
								result, err = svc.handleResponsesStreamingResponse(resp, c, "client-model", "fixture-model", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
							} else {
								result, err = svc.handleResponsesBufferedStreamingResponse(resp, c, "client-model", "fixture-model", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
							}
						}
						if result != nil {
							input, output = result.Usage.InputTokens, result.Usage.OutputTokens
						}
					}
					if tc.fail {
						require.Error(t, err)
						var replay *UpstreamFailoverError
						require.False(t, errors.As(err, &replay))
						require.NotContains(t, rec.Body.String(), "[DONE]")
						require.NotContains(t, rec.Body.String(), "response.completed")
						require.NotContains(t, rec.Body.String(), `"finish_reason":"stop"`)
					} else {
						require.NoError(t, err)
						require.Contains(t, rec.Body.String(), "fixture text")
						require.Contains(t, rec.Body.String(), "client-model")
					}
					require.Equal(t, tc.input, input, "preserve known input even on failure")
					require.Equal(t, tc.output, output, "preserve known output even on failure")
				})
			}
		}
	}
}

func TestIssue985EveryTransportSplit(t *testing.T) {
	wire := "\xef\xbb\xbf" + strings.ReplaceAll(strings.ReplaceAll(issue985Stream(), "\ndata:", "\nid: fixture\n: comment\ndata:"), "\n", "\r\n")
	for split := 0; split <= len(wire); split++ {
		reader := io.MultiReader(strings.NewReader(wire[:split]), strings.NewReader(wire[split:]))
		scanner := bufio.NewScanner(reader)
		scanner.Split(anthropicSSELineSplit())
		st := &anthropicCompatStream{body: io.NopCloser(reader), pump: newAnthropicNativeLinePump(scanner, time.Second), maxLineSize: 4096}
		var usage ClaudeUsage
		for {
			event, payload, ok := st.nextEvent()
			if !ok {
				break
			}
			st.observe(&event, payload)
			if event.Message != nil {
				mergeAnthropicUsage(&usage, event.Message.Usage)
			}
			if event.Usage != nil {
				mergeAnthropicUsage(&usage, *event.Usage)
			}
		}
		st.stop()
		require.Nil(t, st.failure(), "split=%d", split)
		require.Equal(t, 17, usage.InputTokens)
		require.Equal(t, 3, usage.OutputTokens)
	}
}

func TestIssue985FrameBoundAndFieldOrder(t *testing.T) {
	var a anthropicSSEFrameAssembler
	_, _, ready, err := a.line("data: {\"type\":\"ping\"}", 100)
	require.NoError(t, err)
	require.False(t, ready)
	_, _, _, err = a.line("event:ping", 100)
	require.NoError(t, err)
	name, payload, ready, err := a.line("", 100)
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, "ping", name)
	require.Equal(t, `{"type":"ping"}`, payload)
	for i := 0; i < 10; i++ {
		_, _, _, err = a.line(":0123456789", 100)
		if err != nil {
			break
		}
	}
	require.ErrorIs(t, err, errAnthropicSSEFrameTooLarge, "metadata-only frame must also be bounded")
}

func TestIssue985NativeRelays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	canonical := issue985Stream()
	for _, tc := range []struct {
		name, wire string
		fail       bool
	}{
		{"multidata", strings.ReplaceAll(canonical, `,"`, ",\ndata: \""), false},
		{"unknown_multidata", "event: fixture_extension\ndata: opaque one\ndata: opaque two\n\n" + canonical, false},
		{"empty", "", true},
		{"missing", strings.ReplaceAll(strings.ReplaceAll(canonical, `,"usage":{"input_tokens":17,"output_tokens":0}`, ""), `,"usage":{"output_tokens":3}`, ""), true},
		{"error_after_terminal", canonical + issue985Frame("error", `{"type":"error","error":{"type":"overloaded_error","message":"fixture"}}`), true},
		{"reasoning", strings.ReplaceAll(strings.ReplaceAll(canonical, `"type":"text","text":""`, `"type":"thinking","thinking":""`), `"type":"text_delta","text":"fixture text"`, `"type":"thinking_delta","thinking":"fixture reasoning"`), false},
	} {
		for _, route := range []string{"native", "passthrough", "normal"} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.wire))}
				account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
				svc := &GatewayService{rateLimitService: &RateLimitService{}}
				var err error
				switch route {
				case "normal":
					_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "fixture-model", "fixture-model", false)
				case "passthrough":
					_, err = svc.handleStreamingResponseAnthropicAPIKeyPassthrough(c.Request.Context(), resp, c, account, time.Now(), "fixture-model")
				default:
					_, err = (&OpenAIGatewayService{}).handleNativeAnthropicStreamingResponse(c.Request.Context(), resp, c, account, "fixture-model", "fixture-model", "fixture-model", nil, time.Now())
				}
				if tc.fail {
					require.Error(t, err)
					require.NotContains(t, rec.Body.String(), "event: message_stop")
					require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error"))
				} else {
					require.NoError(t, err)
					require.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_stop"))
					require.Contains(t, rec.Body.String(), "fixture")
					if tc.name == "unknown_multidata" {
						require.Contains(t, rec.Body.String(), "event: fixture_extension\ndata: opaque one\ndata: opaque two\n\n")
					}
				}
			})
		}
	}
}

func TestIssue985CancellationResources(t *testing.T) {
	for _, cancelClient := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelClient), func(t *testing.T) {
			reader, writer := io.Pipe()
			defer writer.Close()
			scanner := bufio.NewScanner(reader)
			scanner.Split(anthropicSSELineSplit())
			st := &anthropicCompatStream{body: reader, pump: newAnthropicNativeLinePump(scanner, 30*time.Millisecond), maxLineSize: 128}
			if cancelClient {
				gone := make(chan struct{})
				close(gone)
				st.clientGone = gone
			}
			_, _, ok := st.nextEvent()
			require.False(t, ok)
			failure := st.failure()
			require.NotNil(t, failure)
			if cancelClient {
				require.ErrorIs(t, failure.err, context.Canceled)
			} else {
				require.ErrorIs(t, failure.err, errAnthropicNativeStreamIdle)
			}
			st.stop()
			_, err := io.WriteString(writer, "fixture")
			require.Error(t, err, "upstream body remains open")
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			for {
				select {
				case _, ok := <-st.pump.events:
					if !ok {
						return
					}
				case <-timer.C:
					t.Fatal("reader goroutine leaked")
					return
				}
			}
		})
	}
}

func TestIssue985FinalWriteFailure(t *testing.T) {
	for route, handle := range compatStreamHandlers() {
		t.Run(route, func(t *testing.T) {
			marker := `"finish_reason":"stop"`
			if route == "responses" {
				marker = "response.completed"
			}
			writer := &disconnectOnMarkerResponseWriter{ResponseRecorder: httptest.NewRecorder(), marker: marker}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			result, err := handle(&GatewayService{}, &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(issue985Stream()))}, c)
			require.ErrorIs(t, err, context.Canceled)
			require.NotNil(t, result)
			require.True(t, result.ClientDisconnect)
			require.Equal(t, 17, result.Usage.InputTokens)
			require.Equal(t, 3, result.Usage.OutputTokens)
			require.Zero(t, writer.writesAfterError)
			require.Zero(t, writer.flushesAfterError)
			require.NotContains(t, writer.Body.String(), "[DONE]")
		})
	}
}

func TestIssue985RawPartialUsage(t *testing.T) {
	for _, route := range []string{"native", "passthrough", "normal"} {
		t.Run(route, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			wire := issue985Frame("message_start", `{"type":"message_start","message":{"id":"fixture","type":"message","role":"assistant","model":"fixture","content":[],"usage":{"input_tokens":17,"output_tokens":2,"cache_read_input_tokens":7,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_1h_input_tokens":5}}}}`) + issue985Frame("error", `{"type":"error","error":{"type":"overloaded_error","message":"fixture"}}`)
			resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
			account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
			svc := &GatewayService{rateLimitService: &RateLimitService{}}
			var output, cached, created int
			var err error
			if route == "native" {
				var res *OpenAIForwardResult
				res, err = (&OpenAIGatewayService{}).handleNativeAnthropicStreamingResponse(c.Request.Context(), resp, c, account, "fixture", "fixture", "fixture", nil, time.Now())
				require.NotNil(t, res)
				output, cached, created = res.Usage.OutputTokens, res.Usage.CacheReadInputTokens, res.Usage.CacheCreationInputTokens
				require.Equal(t, 5, res.Usage.CacheCreation1hTokens)
			} else {
				var res *streamingResult
				if route == "normal" {
					res, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "fixture", "fixture", false)
				} else {
					res, err = svc.handleStreamingResponseAnthropicAPIKeyPassthrough(c.Request.Context(), resp, c, account, time.Now(), "fixture")
				}
				require.NotNil(t, res)
				output, cached, created = res.usage.OutputTokens, res.usage.CacheReadInputTokens, res.usage.CacheCreationInputTokens
				require.Equal(t, 5, res.usage.CacheCreation1hTokens)
			}
			require.Error(t, err)
			require.Equal(t, 2, output)
			require.Equal(t, 7, cached)
			require.Equal(t, 5, created)
		})
	}
}

func TestIssue985CanceledBeforeBufferedRead(t *testing.T) {
	for _, route := range []string{"chat", "responses", "native_chat", "native_responses"} {
		t.Run(route, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
			resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(issue985Stream()))}
			var err error
			var input, output int
			var disconnected bool
			if strings.HasPrefix(route, "native") {
				var result *OpenAIForwardResult
				svc := &OpenAIGatewayService{}
				if route == "native_chat" {
					result, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, "fixture", "fixture", "fixture", nil, time.Now())
				} else {
					result, err = svc.handleResponsesBufferedFromNativeAnthropic(resp, c, "fixture", "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				}
				require.NotNil(t, result)
				input, output, disconnected = result.Usage.InputTokens, result.Usage.OutputTokens, result.ClientDisconnect
			} else {
				var result *ForwardResult
				svc := &GatewayService{}
				if route == "chat" {
					result, err = svc.handleCCBufferedFromAnthropic(resp, c, "fixture", "fixture", nil, time.Now())
				} else {
					result, err = svc.handleResponsesBufferedStreamingResponse(resp, c, "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				}
				require.NotNil(t, result)
				input, output, disconnected = result.Usage.InputTokens, result.Usage.OutputTokens, result.ClientDisconnect
			}
			require.ErrorIs(t, err, context.Canceled)
			require.True(t, disconnected)
			require.Equal(t, 17, input)
			require.Equal(t, 3, output)
			require.Empty(t, rec.Body.String(), "already canceled clients receive no additional writes")
		})
	}
}

func TestIssue985BufferedFinalWriteFailure(t *testing.T) {
	for _, route := range []string{"chat", "responses", "native_chat", "native_responses"} {
		t.Run(route, func(t *testing.T) {
			writer := &disconnectOnMarkerResponseWriter{ResponseRecorder: httptest.NewRecorder(), marker: "fixture text"}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(issue985Stream()))}
			var err error
			var input, output int
			var disconnected bool
			if strings.HasPrefix(route, "native") {
				var result *OpenAIForwardResult
				svc := &OpenAIGatewayService{}
				if route == "native_chat" {
					result, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, "fixture", "fixture", "fixture", nil, time.Now())
				} else {
					result, err = svc.handleResponsesBufferedFromNativeAnthropic(resp, c, "fixture", "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				}
				require.NotNil(t, result)
				input, output, disconnected = result.Usage.InputTokens, result.Usage.OutputTokens, result.ClientDisconnect
			} else {
				var result *ForwardResult
				svc := &GatewayService{}
				if route == "chat" {
					result, err = svc.handleCCBufferedFromAnthropic(resp, c, "fixture", "fixture", nil, time.Now())
				} else {
					result, err = svc.handleResponsesBufferedStreamingResponse(resp, c, "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				}
				require.NotNil(t, result)
				input, output, disconnected = result.Usage.InputTokens, result.Usage.OutputTokens, result.ClientDisconnect
			}
			require.Error(t, err)
			require.True(t, disconnected)
			require.Equal(t, 17, input)
			require.Equal(t, 3, output)
			require.True(t, writer.failed)
			require.Zero(t, writer.writesAfterError)
			require.Zero(t, writer.flushesAfterError)
		})
	}
}
