package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const issue776RateLimits = `{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":1}}}`
const issue776Metadata = `{"type":"codex.response.metadata","metadata":{"synthetic":true}}`

// The actual Forward pipeline and pool consume these peer frames. Only the
// upstream peer is synthetic; a close frame must not count as a response terminal.
type issue776CloseConn struct {
	*openAIWSCaptureConn
	beforeClose func()
	closeErr    error
}

func (c *issue776CloseConn) ReadMessage(ctx context.Context) ([]byte, error) {
	message, err := c.openAIWSCaptureConn.ReadMessage(ctx)
	if errors.Is(err, io.EOF) {
		if c.beforeClose != nil {
			c.beforeClose()
			c.beforeClose = nil
		}
		return nil, c.closeErr
	}
	return message, err
}

func (c *issue776CloseConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	message, err := c.ReadMessage(ctx)
	return coderws.MessageText, message, err
}

func issue776Forward(t *testing.T, events []string, beforeClose func(*gin.Context)) (*httptest.ResponseRecorder, *gin.Context, *OpenAIForwardResult, *issue776CloseConn, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	conn := &issue776CloseConn{
		openAIWSCaptureConn: &openAIWSCaptureConn{},
		closeErr:            coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "synthetic-private-close-reason"},
	}
	for _, event := range events {
		conn.events = append(conn.events, []byte(event))
	}
	if beforeClose != nil {
		conn.beforeClose = func() { beforeClose(c) }
	}
	dialer := &wsSSETestDialer{conn: conn}
	httpUpstream := &httpUpstreamRecorder{}
	s := wsSSETestService(t, dialer, httpUpstream)
	result, err := s.Forward(context.Background(), c, wsSSETestAccount(), []byte(wsSSETestRequest))
	require.Nil(t, httpUpstream.lastReq, "a response.create already sent to WS cannot replay over HTTP")
	require.EqualValues(t, 1, dialer.calls.Load(), "an incomplete turn cannot reconnect and resubmit")
	conn.mu.Lock()
	require.Len(t, conn.writes, 1)
	require.Equal(t, "response.create", conn.writes[0]["type"])
	conn.mu.Unlock()
	return w, c, result, conn, err
}

func TestOpenAIWSSSEIncompleteControlMetadataDoesNotCommitSuccess(t *testing.T) {
	for _, events := range [][]string{nil, {issue776RateLimits}, {issue776RateLimits, issue776Metadata}} {
		name := "zero_events"
		if len(events) == 1 {
			name = "one_control_event"
		} else if len(events) == 2 {
			name = "two_control_events_then_1000"
		}
		t.Run(name, func(t *testing.T) {
			w, c, result, conn, err := issue776Forward(t, events, func(c *gin.Context) {
				require.False(t, c.Writer.Written(), "control metadata must not commit HTTP 200")
			})
			require.Error(t, err)
			var incomplete *OpenAIWSSSEIncompleteError
			require.ErrorAs(t, err, &incomplete)
			require.True(t, incomplete.RequestWritten)
			require.True(t, incomplete.ResponseWritten)
			require.False(t, incomplete.DownstreamCommitted)
			require.Equal(t, 1000, incomplete.CloseCode)
			require.Equal(t, len(events), incomplete.EventCount)
			require.Zero(t, incomplete.TokenEventCount)
			require.Zero(t, incomplete.TerminalEventCount)
			require.Nil(t, result, "unknown consumption must not invent a zero-token usage record")
			require.Equal(t, http.StatusBadGateway, w.Code)
			require.Contains(t, w.Header().Get("Content-Type"), "application/json")
			require.Equal(t, OpenAIWSSSEIncompleteCode, gjson.Get(w.Body.String(), "error.code").String())
			require.NotContains(t, w.Body.String(), "codex.rate_limits")
			require.NotContains(t, w.Body.String(), "synthetic-private-close-reason")
			require.NotContains(t, err.Error(), "synthetic-private-close-reason")
			require.NotContains(t, c.GetString(OpsUpstreamErrorDetailKey), "synthetic-private-close-reason")
			conn.mu.Lock()
			require.True(t, conn.closed, "an incomplete upstream connection must be discarded")
			conn.mu.Unlock()
		})
	}
}

func TestOpenAIWSSSEIncompleteAfterOutputHasOneFailedTerminal(t *testing.T) {
	for _, event := range []string{
		`{"type":"response.created","sequence_number":12,"response":{"id":"resp_issue776"}}`,
		`{"type":"response.output_text.delta","delta":"synthetic text"}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"synthetic_call","name":"lookup"}}`,
		`{"type":"vendor.unknown_event","data":"synthetic"}`,
	} {
		t.Run(gjson.Get(event, "type").String(), func(t *testing.T) {
			w, c, _, conn, err := issue776Forward(t, []string{issue776RateLimits, issue776Metadata, event}, func(c *gin.Context) {
				require.True(t, c.Writer.Written(), "only the two known control event types may be delayed")
			})
			require.Error(t, err)
			conn.mu.Lock()
			outboundModel, _ := conn.writes[0]["model"].(string)
			conn.mu.Unlock()
			require.Equal(t, "gpt-5.4", outboundModel, "OAuth normalizes the actual outbound model while preserving the client alias below")
			require.Equal(t, http.StatusOK, w.Code, "committed HTTP status cannot be replaced after output")
			messages := issue776SSEMessages(t, w.Body.String())
			require.Len(t, messages, 4)
			require.JSONEq(t, issue776RateLimits, string(messages[0]))
			require.JSONEq(t, issue776Metadata, string(messages[1]))
			require.JSONEq(t, event, string(messages[2]))
			require.Equal(t, "response.failed", gjson.GetBytes(messages[3], "type").String())
			require.Equal(t, OpenAIWSSSEIncompleteCode, gjson.GetBytes(messages[3], "response.error.code").String())
			require.True(t, gjson.GetBytes(messages[3], "sequence_number").Exists())
			if gjson.Get(event, "sequence_number").Exists() {
				require.Greater(t, gjson.GetBytes(messages[3], "sequence_number").Int(), gjson.Get(event, "sequence_number").Int())
			}
			require.Equal(t, "gpt-5.1", gjson.GetBytes(messages[3], "response.model").String())
			require.Greater(t, gjson.GetBytes(messages[3], "response.created_at").Int(), int64(0))
			streamErr, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.True(t, streamErr.CountTowardsSLA)
			require.Equal(t, OpenAIWSSSEIncompleteCode, streamErr.Code)
			require.Equal(t, http.StatusBadGateway, streamErr.IntendedStatus)
		})
	}
}

func TestOpenAIWSSSEIncompleteControlMetadataSuccessPreservesOrder(t *testing.T) {
	terminal := `{"type":"response.completed","response":{"id":"resp_issue776","model":"gpt-5.1","status":"completed","usage":{"input_tokens":7,"output_tokens":0}}}`
	w, c, result, _, err := issue776Forward(t, []string{issue776RateLimits, issue776Metadata, terminal}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Nil(t, result.FirstTokenMs, "control and terminal-only events do not invent TTFT")
	require.Equal(t, 7, result.Usage.InputTokens)
	messages := issue776SSEMessages(t, w.Body.String())
	require.Len(t, messages, 3)
	for i, want := range []string{issue776RateLimits, issue776Metadata, terminal} {
		require.JSONEq(t, want, string(messages[i]))
	}
	_, failed := GetOpsStreamError(c)
	require.False(t, failed)
}

func TestOpenAIWSSSEIncompleteMetadataThenOverloadCannotEnableFailover(t *testing.T) {
	event := `{"type":"error","error":{"type":"server_error","code":"server_is_overloaded","message":"synthetic busy"}}`
	w, _, _, _, err := issue776Forward(t, []string{issue776RateLimits, issue776Metadata, event}, nil)
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover), "buffering must not turn an already-started metadata stream into a new account attempt")
	require.Contains(t, w.Body.String(), "server_is_overloaded")
	messages := issue776SSEMessages(t, w.Body.String())
	require.GreaterOrEqual(t, len(messages), 3)
	require.JSONEq(t, issue776RateLimits, string(messages[0]))
	require.JSONEq(t, issue776Metadata, string(messages[1]))
}

func TestOpenAIWSSSEIncompleteControlMetadataBufferHasFiniteBounds(t *testing.T) {
	for _, events := range [][]string{
		func() []string {
			var many []string
			for range 20 {
				many = append(many, issue776RateLimits)
			}
			return many
		}(),
		{`{"type":"codex.response.metadata","padding":"` + strings.Repeat("x", 100*1024) + `"}`},
	} {
		name := "event_count"
		if len(events) == 1 {
			name = "byte_count"
		}
		t.Run(name, func(t *testing.T) {
			w, _, _, _, err := issue776Forward(t, events, func(c *gin.Context) {
				require.True(t, c.Writer.Written(), "excess control metadata must flush instead of growing an unbounded buffer")
			})
			require.Error(t, err)
			messages := issue776SSEMessages(t, w.Body.String())
			require.Len(t, messages, len(events)+1)
			for i, want := range events {
				require.JSONEq(t, want, string(messages[i]))
			}
		})
	}
}

func TestOpenAIWSSSEIncompletePreservesObservedUsage(t *testing.T) {
	event := `{"type":"response.in_progress","response":{"id":"resp_usage_issue776","usage":{"input_tokens":9,"output_tokens":2,"input_tokens_details":{"cached_tokens":4}}}}`
	_, _, result, _, err := issue776Forward(t, []string{issue776RateLimits, event}, nil)
	require.Error(t, err)
	require.NotNil(t, result, "explicit upstream usage must survive an incomplete stream")
	require.True(t, result.streamReadIncomplete)
	require.Equal(t, 9, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Empty(t, result.UpstreamTerminalEvent, "usage evidence does not turn an incomplete response into success")
}

func TestOpenAIWSSSEIncompleteRawEOFCannotReplay(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	conn := &openAIWSCaptureConn{events: [][]byte{[]byte(issue776RateLimits), []byte(issue776Metadata)}}
	dialer := &wsSSETestDialer{conn: conn}
	httpUpstream := &httpUpstreamRecorder{}
	s := wsSSETestService(t, dialer, httpUpstream)
	result, err := s.Forward(context.Background(), c, wsSSETestAccount(), []byte(wsSSETestRequest))
	var incomplete *OpenAIWSSSEIncompleteError
	require.ErrorAs(t, err, &incomplete)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, -1, incomplete.CloseCode)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Equal(t, OpenAIWSSSEIncompleteCode, gjson.Get(w.Body.String(), "error.code").String())
	require.Nil(t, httpUpstream.lastReq)
	require.EqualValues(t, 1, dialer.calls.Load())
	conn.mu.Lock()
	defer conn.mu.Unlock()
	require.Len(t, conn.writes, 1)
}

func TestOpenAIWSSSEIncompleteMetadataCancellationKeepsClientDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	conn := &issue776CloseConn{
		openAIWSCaptureConn: &openAIWSCaptureConn{events: [][]byte{[]byte(issue776RateLimits), []byte(issue776Metadata)}},
		beforeClose:         cancel,
		closeErr:            context.Canceled,
	}
	dialer := &wsSSETestDialer{conn: conn}
	httpUpstream := &httpUpstreamRecorder{}
	s := wsSSETestService(t, dialer, httpUpstream)
	result, err := s.Forward(ctx, c, wsSSETestAccount(), []byte(wsSSETestRequest))
	require.ErrorIs(t, err, context.Canceled)
	var incomplete *OpenAIWSSSEIncompleteError
	require.False(t, errors.As(err, &incomplete), "client cancellation must not become provider stream failure")
	require.Nil(t, result, "a canceled metadata-only request has no observed usage to record")
	require.NotContains(t, w.Body.String(), "response.failed")
	require.Nil(t, httpUpstream.lastReq)
	require.EqualValues(t, 1, dialer.calls.Load())
	conn.mu.Lock()
	defer conn.mu.Unlock()
	require.Len(t, conn.writes, 1)
}

func TestOpenAIWSSSEIncompleteZeroEventCapacityRejectionKeepsExistingFailover(t *testing.T) {
	event := `{"type":"error","error":{"type":"server_error","code":"server_is_overloaded","message":"synthetic busy"}}`
	w, _, _, _, err := issue776Forward(t, []string{event}, nil)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover, "an explicit capacity rejection before any other event retains its existing policy")
	require.Equal(t, http.StatusServiceUnavailable, failover.StatusCode)
	require.Empty(t, w.Body.String(), "the outer handler still owns the unchanged capacity decision")
}

func TestOpenAIWSSSEIncompletePreservesObservedImageWithoutTokenUsage(t *testing.T) {
	event := `{"type":"response.output_item.done","item":{"id":"ig_issue776","type":"image_generation_call","status":"completed","result":"synthetic-image-data"}}`
	_, _, result, _, err := issue776Forward(t, []string{issue776RateLimits, event}, nil)
	require.Error(t, err)
	require.NotNil(t, result, "a delivered image is concrete output evidence even without token usage")
	require.True(t, result.streamReadIncomplete)
	require.Equal(t, 1, result.ImageCount)
	require.Empty(t, result.UpstreamTerminalEvent)
}

func issue776SSEMessages(t *testing.T, body string) []json.RawMessage {
	t.Helper()
	var messages []json.RawMessage
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			message := []byte(strings.TrimPrefix(line, "data: "))
			require.True(t, json.Valid(message), "every emitted data frame must be JSON")
			messages = append(messages, json.RawMessage(message))
		}
	}
	return messages
}
