//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const http2FollowupCCPartial = "data: {\"id\":\"synthetic\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"}}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n"
const http2FollowupSemanticData = `{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"synthetic semantic failure"}}`
const http2FollowupNativePartial = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"model\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"output_tokens\":1}}\n\n"

func http2FollowupCCConvert(t *testing.T, mode, payload string) (*httptest.ResponseRecorder, *gin.Context, *OpenAIForwardResult, error) {
	t.Helper()
	resp := http2PeerResetResponse(t, "text/event-stream", payload)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+mode, nil)
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
	var result *OpenAIForwardResult
	var err error
	if mode == "responses" {
		result, err = svc.streamChatCompletionsAsResponses(c, resp, rawChatCompletionsTestAccount(), "model", nil, nil, false, nil, "model", "model", nil, nil, false, time.Now(), nil)
	} else {
		result, err = svc.streamChatCompletionsAsAnthropic(c, resp, rawChatCompletionsTestAccount(), "model", "model", "model", nil, nil, time.Now())
	}
	return rec, c, result, err
}

func TestHTTP2CCSemanticFailureBeforeReset(t *testing.T) {
	for _, mode := range []string{"responses", "messages"} {
		for _, envelope := range []string{"event_error", "data_error", "multiline_error", "code_only_error"} {
			t.Run(mode+"/"+envelope, func(t *testing.T) {
				errorEvent := "data: " + http2FollowupSemanticData + "\n\n"
				if envelope == "event_error" {
					errorEvent = "event: error\n" + errorEvent
				} else if envelope == "multiline_error" {
					errorEvent = "event: error\ndata: {\ndata: \"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"synthetic semantic failure\"}}\n\n"
				} else if envelope == "code_only_error" {
					errorEvent = strings.ReplaceAll(errorEvent, `"type":"invalid_request_error",`, "")
				}
				rec, c, result, err := http2FollowupCCConvert(t, mode, http2FollowupCCPartial+errorEvent)
				require.Error(t, err)
				require.NotNil(t, result)
				require.Contains(t, rec.Body.String(), "context_length_exceeded", "F4 complete CC semantic error must survive a later HTTP2 reset: %s/%s", mode, envelope)
				require.Contains(t, rec.Body.String(), "synthetic semantic failure")
				require.Contains(t, rec.Body.String(), "partial")
				require.Equal(t, 3, result.Usage.InputTokens)
				require.Equal(t, 1, result.Usage.OutputTokens)
				require.Equal(t, http.StatusOK, rec.Code)
				marker, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.Nil(t, marker.TransportFailure)
				require.Equal(t, "invalid_request_error", marker.ErrType)
				require.True(t, marker.RequestScoped)
				require.Equal(t, "context_length_exceeded", marker.Code)
				require.Equal(t, http.StatusBadRequest, marker.IntendedStatus)
				_, _, reset := OpenAIUpstreamStreamReadErrorDetails(err)
				require.False(t, reset)
				var failover *UpstreamFailoverError
				require.NotErrorAs(t, err, &failover)
				assertHTTP2FollowupTerminal(t, rec.Body.String(), mode, "context_length_exceeded")
			})
		}
	}
}

func TestHTTP2CCIncompleteSemanticEventStillResets(t *testing.T) {
	for _, mode := range []string{"responses", "messages"} {
		for i, fragment := range []string{
			"event: error\n",
			"event: error\ndata: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"cut",
			"event: error\ndata: " + http2FollowupSemanticData + "\n",
			"data: " + http2FollowupSemanticData,
			"event: error\ndata: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"cut\n\n",
		} {
			t.Run(fmt.Sprintf("%s/%d", mode, i), func(t *testing.T) {
				rec, c, result, err := http2FollowupCCConvert(t, mode, http2FollowupCCPartial+fragment)
				require.Error(t, err)
				require.NotNil(t, result)
				code, _, reset := OpenAIUpstreamStreamReadErrorDetails(err)
				require.True(t, reset, "incomplete CC semantic event must still record reset")
				require.Equal(t, OpenAIUpstreamHTTP2StreamErrorCode, code)
				marker, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.NotNil(t, marker.TransportFailure)
				require.NotContains(t, rec.Body.String(), "context_length_exceeded")
				require.Equal(t, 3, result.Usage.InputTokens)
				require.Equal(t, 1, result.Usage.OutputTokens)
				assertHTTP2FollowupTerminal(t, rec.Body.String(), mode, OpenAIUpstreamHTTP2StreamErrorCode)
			})
		}
	}
}

func TestHTTP2CCNonContextSemanticStatusUnchanged(t *testing.T) {
	for _, mode := range []string{"responses", "messages"} {
		t.Run(mode, func(t *testing.T) {
			// Generic provider validation keeps the existing semantic formatter's
			// status policy; the new 400 normalization is limited to context.
			payload := http2FollowupCCPartial + "event: error\ndata: {\"error\":{\"type\":\"invalid_request_error\",\"code\":\"invalid_encrypted_content\",\"message\":\"Encrypted content could not be verified\"}}\n\n"
			rec, c, _, err := http2FollowupCCConvert(t, mode, payload)
			require.Error(t, err)
			marker, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.Nil(t, marker.TransportFailure)
			require.Equal(t, http.StatusBadGateway, marker.IntendedStatus)
			require.False(t, marker.RequestScoped)
			require.Equal(t, "invalid_encrypted_content", marker.Code)
			assertHTTP2FollowupTerminal(t, rec.Body.String(), mode, "invalid_encrypted_content")
		})
	}
}

func TestHTTP2NativeRelayRequiresCompleteTerminal(t *testing.T) {
	for name, fragment := range map[string]string{
		"event_only":       "event: message_stop\n",
		"event_no_data":    "event: message_stop\n\n",
		"truncated_data":   "event: message_stop\ndata: {\"type\":\"message_stop\",\"cut\":",
		"missing_boundary": "event: message_stop\ndata: {\"type\":\"message_stop\"}\n",
		"data_only_cut":    "data: {\"type\":\"message_stop\"}",
		"invalid_json":     "event: message_stop\ndata: {\"type\":\"message_stop\",\"cut\":\n\n",
		"wrong_data_type":  "event: message_stop\ndata: {\"type\":\"ping\"}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			resp := http2PeerResetResponse(t, "text/event-stream", http2FollowupNativePartial+fragment)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			result, err := (&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}).handleNativeAnthropicStreamingResponse(context.Background(), resp, c, nativeAnthropicTestAccount(), "model", "model", "model", nil, time.Now())
			require.Error(t, err, "F5 incomplete native message_stop must not swallow a later HTTP2 reset: %s", name)
			code, _, reset := OpenAIUpstreamStreamReadErrorDetails(err)
			require.True(t, reset)
			require.Equal(t, OpenAIUpstreamHTTP2StreamErrorCode, code)
			marker, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.NotNil(t, marker.TransportFailure)
			require.NotNil(t, result)
			require.Equal(t, 3, result.Usage.InputTokens)
			require.Equal(t, 1, result.Usage.OutputTokens)
			require.NotContains(t, rec.Body.String(), "message_stop")
			assertHTTP2FollowupTerminal(t, rec.Body.String(), "messages", OpenAIUpstreamHTTP2StreamErrorCode)
		})
	}
}

func TestHTTP2NativeRelayCompleteTerminalControls(t *testing.T) {
	for _, name := range []string{"success", "data_only_success", "semantic_error"} {
		t.Run(name, func(t *testing.T) {
			terminal := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			if name == "data_only_success" {
				terminal = "data: {\"type\":\"message_stop\"}\n\n"
			} else if name == "semantic_error" {
				terminal = "event: error\ndata: " + http2FollowupSemanticData + "\n\n"
			}
			resp := http2PeerResetResponse(t, "text/event-stream", http2FollowupNativePartial+terminal)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			result, err := (&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}).handleNativeAnthropicStreamingResponse(context.Background(), resp, c, nativeAnthropicTestAccount(), "model", "model", "model", nil, time.Now())
			if name == "semantic_error" {
				require.Error(t, err)
				require.Contains(t, rec.Body.String(), "context_length_exceeded", "F5 complete native semantic failure must survive a later HTTP2 reset")
				marker, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.Nil(t, marker.TransportFailure)
				require.Equal(t, "context_length_exceeded", marker.Code)
				assertHTTP2FollowupTerminal(t, rec.Body.String(), "messages", "context_length_exceeded")
			} else {
				require.NoError(t, err)
				_, marked := GetOpsStreamError(c)
				require.False(t, marked)
				require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"message_stop"`))
				require.NotContains(t, rec.Body.String(), "event: error")
			}
			require.NotNil(t, result)
			require.Equal(t, 3, result.Usage.InputTokens)
			require.Equal(t, 1, result.Usage.OutputTokens)
			require.NotContains(t, rec.Body.String(), "upstream_http2_stream_error")
		})
	}
}

func assertHTTP2FollowupTerminal(t *testing.T, body, mode, code string) {
	t.Helper()
	terminal, codePath := "event: error\n", "error.code"
	if mode == "responses" {
		terminal, codePath = "event: response.failed\n", "response.error.code"
	}
	require.Equal(t, 1, strings.Count(body, terminal))
	for _, forbidden := range []string{"response.completed", "message_stop", "[DONE]"} {
		require.NotContains(t, body, forbidden)
	}
	matched := false
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := []byte(strings.TrimPrefix(line, "data: "))
		require.True(t, json.Valid(payload), "every relayed data line must be complete JSON")
		if gjson.GetBytes(payload, codePath).String() == code {
			matched = true
			if mode == "messages" {
				require.Equal(t, "error", gjson.GetBytes(payload, "type").String())
			}
		}
	}
	require.True(t, matched, "one proper terminal must carry the original error code")
}
