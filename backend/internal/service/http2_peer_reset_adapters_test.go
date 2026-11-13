//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHTTP2PeerResetWebSocketHTTPBridge(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_output", true: "after_output"}[partial], func(t *testing.T) {
			data := ""
			if partial {
				data = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &openAIStreamReadThenErrorCloser{reader: strings.NewReader(data), err: errors.New("stream error: stream ID 77; INTERNAL_ERROR; received from peer")}}}
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}, httpUpstream: upstream}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			SetOpenAIClientTransport(c, OpenAIClientTransportWS)
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
			payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)
			var frames []string
			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "synthetic", payload, len(payload), "gpt-5.1", "", "", "", "", 1, func(frame []byte) error { frames = append(frames, string(frame)); return nil })
			require.Error(t, err)
			require.NotNil(t, result)
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			code, _, ok := OpenAIUpstreamStreamReadErrorDetails(err)
			require.True(t, ok)
			require.Equal(t, OpenAIUpstreamHTTP2StreamErrorCode, code)
			require.Empty(t, rec.Body.String(), "an upgraded WebSocket must not receive HTTP SSE/JSON writes")
			terminals := 0
			for _, frame := range frames {
				if gjson.Get(frame, "type").String() == "response.failed" {
					terminals++
					require.Equal(t, "INTERNAL_ERROR", gjson.Get(frame, "response.error.reset_reason").String())
				}
				require.NotContains(t, frame, "stream ID")
				require.NotContains(t, frame, "response.completed")
			}
			require.Equal(t, 1, terminals)
		})
	}
}

func TestHTTP2PeerResetAnthropicAdapters(t *testing.T) {
	for _, chat := range []bool{false, true} {
		t.Run(map[bool]string{false: "responses_upstream", true: "chat_upstream"}[chat], func(t *testing.T) {
			data := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
			if chat {
				data = "data: {\"id\":\"synthetic\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &openAIStreamReadThenErrorCloser{reader: strings.NewReader(data), err: errors.New("stream error: stream ID 77; INTERNAL_ERROR; received from peer")}}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := []byte(`{"model":"gpt-5.1","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"synthetic"}]}`)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
			account := rawChatCompletionsTestAccount()
			if chat {
				account = forceChatMessagesFallbackAccount()
			} else {
				account.Extra = map[string]any{"openai_responses_supported": true}
			}
			_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
			require.Error(t, err)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error\n"))
			require.Contains(t, rec.Body.String(), `"type":"error"`)
			require.Contains(t, rec.Body.String(), "INTERNAL_ERROR")
			require.NotContains(t, rec.Body.String(), "message_stop")
			require.NotContains(t, rec.Body.String(), "[DONE]")
		})
	}
}

func TestHTTP2PeerResetAfterRawSemanticFailureKeepsOriginalError(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	data := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\nevent: error\ndata: {\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"synthetic rejection\"}}\n\n"
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &openAIStreamReadThenErrorCloser{reader: strings.NewReader(data), err: errors.New("stream error: stream ID 77; INTERNAL_ERROR; received from peer")}}
	_, err := (&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}).streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "gpt-5.1", "gpt-5.1", "gpt-5.1", nil, nil, time.Now(), 10)
	require.Error(t, err)
	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error\n"))
	require.Contains(t, rec.Body.String(), "context_length_exceeded")
	require.NotContains(t, rec.Body.String(), "upstream_http2_stream_error")
	_, _, classified := OpenAIUpstreamStreamReadErrorDetails(err)
	require.False(t, classified)
}

// Delivers a real net/http HTTP/2 body, rather than an error string fixture.
func http2PeerResetResponse(t *testing.T, contentType, payload string) *http.Response {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.ProtoMajor != 2 {
			t.Error("expected actual HTTP/2")
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Sub2api-Plugin-Preflight-Sse", "1")
		_, _ = io.WriteString(w, payload)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	resp, err := server.Client().Post(server.URL, "application/json", strings.NewReader(`{"input":"synthetic"}`))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestHTTP2PeerResetBufferedBoundaries(t *testing.T) {
	for _, name := range []string{"responses_json", "responses_sse", "passthrough_json", "passthrough_sse", "messages_sse", "raw_chat_json", "shared_chat_json"} {
		t.Run(name, func(t *testing.T) {
			contentType, payload := "application/json", `{"id":"partial"`
			if strings.HasSuffix(name, "_sse") {
				contentType = "text/event-stream"
				payload = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
			}
			resp := http2PeerResetResponse(t, contentType, payload)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
			var err error
			switch name {
			case "responses_json", "responses_sse":
				_, err = svc.handleNonStreamingResponse(context.Background(), resp, c, account, "model", "model")
			case "passthrough_json", "passthrough_sse":
				_, err = svc.handleNonStreamingResponsePassthrough(context.Background(), resp, c, account, "model", "model")
			case "messages_sse":
				c.Request.URL.Path = "/v1/messages"
				_, err = svc.handleAnthropicBufferedStreamingResponse(resp, c, account, "model", "model", "model", time.Now())
			case "raw_chat_json":
				c.Request.URL.Path = "/v1/chat/completions"
				_, err = svc.bufferRawChatCompletions(c, resp, account, "model", "model", "model", nil, nil, time.Now())
			case "shared_chat_json":
				_, _, err = svc.readCCUpstreamJSONResponse(c, resp, account, writeOpenAIResponsesFallbackError)
			}
			require.Error(t, err)
			_, marked := GetOpsStreamError(c)
			require.True(t, marked, "buffered reset must establish transport evidence")
			code, _, ok := OpenAIUpstreamStreamReadErrorDetails(err)
			require.True(t, ok)
			require.Equal(t, OpenAIUpstreamHTTP2StreamErrorCode, code)
			require.Equal(t, 502, rec.Code)
			require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
			require.True(t, json.Valid(rec.Body.Bytes()))
			if name == "messages_sse" {
				require.Equal(t, "error", gjson.GetBytes(rec.Body.Bytes(), "type").String())
			}
			require.Contains(t, rec.Body.String(), "Do not automatically retry")
			require.NotContains(t, rec.Body.String(), "stream ID")
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
		})
	}
}

func TestHTTP2PeerResetPluginEarlyMetadata(t *testing.T) {
	for _, path := range []string{"forward", "passthrough"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%v", path, partial), func(t *testing.T) {
				payload := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_synthetic\",\"created_at\":1770000000,\"output\":[]}}\n\n"
				if partial {
					payload += "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
				}
				payload += "event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"unfinished\":"
				resp := http2PeerResetResponse(t, "text/event-stream", payload)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				markOpenAIPluginSSEPreflight(resp, c.Request, &PluginInstallation{PluginKey: openAIWSSSEAcceleratorPluginKey, SignatureStatus: PluginSignatureTrusted})
				svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIPluginSSEEarlyEventsEnabled: true, MaxLineSize: defaultMaxLineSize}}}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
				var err error
				if path == "forward" {
					_, err = svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "model", "model")
				} else {
					_, err = svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "model", "model")
				}
				require.Error(t, err)
				require.Equal(t, 200, rec.Code)
				require.Contains(t, rec.Body.String(), "response.created")
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed\n"), "plugin reset must have exactly one terminal failure")
				require.Contains(t, rec.Body.String(), "upstream_http2_stream_error")
				require.NotContains(t, rec.Body.String(), "unfinished")
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if strings.HasPrefix(line, "data: ") {
						require.True(t, json.Valid([]byte(strings.TrimPrefix(line, "data: "))))
					}
				}
			})
		}
	}
}

func TestHTTP2SemanticImageErrorCannotSpoofReset(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			message := "stream error: stream ID 1; INTERNAL_ERROR; received from peer"
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			if stream {
				c.Header("Content-Type", "text/event-stream")
				_, _ = c.Writer.WriteString(": established\n\n")
				c.Writer.Flush()
			}
			payload, marshalErr := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "code": "invalid_image", "message": message}})
			require.NoError(t, marshalErr)
			var semantic *OpenAIImagesUpstreamError
			if stream {
				semantic = extractOpenAIImagesUpstreamError(append(append([]byte("event: error\ndata: "), payload...), []byte("\n\n")...))
			} else {
				semantic = openAIImagesUpstreamErrorFromHTTP(400, http.Header{}, payload)
			}
			require.NotNil(t, semantic)
			err := (&OpenAIGatewayService{}).handleOpenAIImagesOAuthResponseError(context.Background(), c, &Account{ID: 1, Platform: PlatformOpenAI}, "gpt-image-2", "", &http.Response{StatusCode: 200, Header: http.Header{}}, -1, semantic)
			require.Error(t, err)
			marker, exists := GetOpsStreamError(c)
			data, _ := json.Marshal(marker)
			require.False(t, exists && gjson.GetBytes(data, "TransportFailure.reset_reason").Exists(), "semantic image text must not create trusted reset evidence")
			var original *OpenAIImagesUpstreamError
			require.ErrorAs(t, err, &original)
			require.Same(t, semantic, err, "semantic failure must not be wrapped as transport")
			require.Equal(t, 400, original.StatusCode)
			require.NotContains(t, rec.Body.String(), `"category":"transport_error"`)
		})
	}
}

func TestHTTP2PeerResetNativeUsage(t *testing.T) {
	for _, mode := range []string{"responses_stream", "responses_buffered", "chat_stream", "chat_buffered"} {
		t.Run(mode, func(t *testing.T) {
			payload := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"synthetic\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"partial\"}}\n\n"
			resp := http2PeerResetResponse(t, "text/event-stream", payload)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			var result *OpenAIForwardResult
			var err error
			switch mode {
			case "responses_stream":
				result, err = svc.handleResponsesStreamingFromNativeAnthropic(resp, c, "model", "model", "model", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
			case "responses_buffered":
				result, err = svc.handleResponsesBufferedFromNativeAnthropic(resp, c, "model", "model", "model", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
			case "chat_stream":
				c.Request.URL.Path = "/v1/chat/completions"
				result, err = svc.handleCCStreamingFromNativeAnthropic(resp, c, "model", "model", "model", nil, time.Now())
			case "chat_buffered":
				c.Request.URL.Path = "/v1/chat/completions"
				result, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, "model", "model", "model", nil, time.Now())
			}
			require.Error(t, err, "native reset must return failure instead of synthetic success")
			require.NotNil(t, result)
			require.Equal(t, 3, result.Usage.InputTokens)
			require.Equal(t, 1, result.Usage.OutputTokens)
			require.NotContains(t, rec.Body.String(), "response.completed")
			require.NotContains(t, rec.Body.String(), "[DONE]")
		})
	}
}

func TestHTTP2NativeSemanticErrorBeforeReset(t *testing.T) {
	for _, mode := range []string{"responses_stream", "responses_buffered", "chat_stream", "chat_buffered"} {
		t.Run(mode, func(t *testing.T) {
			payload := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"synthetic\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"partial\"}}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"synthetic semantic failure\"}}\n\n"
			resp := http2PeerResetResponse(t, "text/event-stream", payload)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			var result *OpenAIForwardResult
			var err error
			switch mode {
			case "responses_stream":
				result, err = svc.handleResponsesStreamingFromNativeAnthropic(resp, c, "model", "model", "model", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
			case "responses_buffered":
				result, err = svc.handleResponsesBufferedFromNativeAnthropic(resp, c, "model", "model", "model", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
			case "chat_stream":
				c.Request.URL.Path = "/v1/chat/completions"
				result, err = svc.handleCCStreamingFromNativeAnthropic(resp, c, "model", "model", "model", nil, time.Now())
			case "chat_buffered":
				c.Request.URL.Path = "/v1/chat/completions"
				result, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, "model", "model", "model", nil, time.Now())
			}
			require.Error(t, err, "native semantic failure must not become successful completion")
			require.NotNil(t, result)
			require.Equal(t, 3, result.Usage.InputTokens)
			require.Equal(t, 1, result.Usage.OutputTokens)
			require.Contains(t, rec.Body.String(), "context_length_exceeded")
			require.Contains(t, rec.Body.String(), "synthetic semantic failure")
			for _, forbidden := range []string{"response.completed", "[DONE]", "upstream_http2_stream_error", "transport_error"} {
				require.NotContains(t, rec.Body.String(), forbidden)
			}
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			if strings.HasSuffix(mode, "_stream") {
				require.Equal(t, 200, rec.Code)
				terminal := "event: response.failed\n"
				if strings.HasPrefix(mode, "chat_") {
					terminal = "event: error\n"
				}
				require.Equal(t, 1, strings.Count(rec.Body.String(), terminal))
			} else {
				require.GreaterOrEqual(t, rec.Code, 400)
			}
		})
	}
}

func TestHTTP2RawIncompleteErrorEventStillResets(t *testing.T) {
	for _, fragment := range []string{"event: error\ndata: {\"error\":{\"message\":\"cut", "event: error\ndata: {\"error\":{\"message\":\"complete JSON without event boundary\"}}"} {
		t.Run(fmt.Sprint(len(fragment)), func(t *testing.T) {
			payload := "data: {\"id\":\"synthetic\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n" + fragment
			resp := http2PeerResetResponse(t, "text/event-stream", payload)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			_, err := (&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}).streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "model", "model", "model", nil, nil, time.Now(), 10)
			require.Error(t, err)
			code, _, classified := OpenAIUpstreamStreamReadErrorDetails(err)
			require.True(t, classified, "incomplete raw error event must not suppress reset classification")
			require.Equal(t, OpenAIUpstreamHTTP2StreamErrorCode, code)
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error\n"))
			require.Contains(t, rec.Body.String(), "partial")
			require.Contains(t, rec.Body.String(), "Do not automatically retry")
			require.NotContains(t, rec.Body.String(), "cut")
			require.NotContains(t, rec.Body.String(), "complete JSON without event boundary")
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if strings.HasPrefix(line, "data: ") {
					require.True(t, json.Valid([]byte(strings.TrimPrefix(line, "data: "))), "reset terminal must follow complete JSON events only")
				}
			}
		})
	}
}
