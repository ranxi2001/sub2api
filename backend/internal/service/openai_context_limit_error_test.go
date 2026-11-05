//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIContextLimitHTTP502Returns400(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			c, rec := newNonStreamingFailoverContext(t)
			svc := newNonStreamingFailoverService()
			account := newNonStreamingFailoverAccount()
			// AuthConnectionID would touch credential recovery on a generic 502.
			account.AuthConnectionID = "auth-test"
			body := []byte(`{"error":{"type":"upstream_error","code":"context_length_exceeded","param":"input","message":"Input exceeds the context window"},"echo":"private input"}`)
			resp := &http.Response{StatusCode: 502, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}
			var err error
			if passthrough {
				err = svc.handleErrorResponsePassthrough(context.Background(), resp, c, account, nil, body)
			} else {
				_, err = svc.handleErrorResponse(context.Background(), resp, c, account, nil)
			}
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.Equal(t, 400, rec.Code)
			require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
			require.Equal(t, "context_length_exceeded", gjson.Get(rec.Body.String(), "error.code").String())
			require.Equal(t, "input", gjson.Get(rec.Body.String(), "error.param").String())
			require.NotContains(t, rec.Body.String(), "private input")
			require.Equal(t, 502, c.GetInt(OpsUpstreamStatusCodeKey))
			require.Contains(t, c.GetString(OpsUpstreamErrorMessageKey), "context_length_exceeded")
		})
	}
}

func TestOpenAIContextLimitNonStreamingSSETerminals(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, event := range []string{"response.failed", "error"} {
			name := event
			if passthrough {
				name += "/passthrough"
			}
			t.Run(name, func(t *testing.T) {
				c, rec := newNonStreamingFailoverContext(t)
				svc := newNonStreamingFailoverService()
				// The code alone must suffice; do not rely on the message repeating it.
				payload := `{"type":"` + event + `","response":{"error":{"code":"context_length_exceeded","message":"Too many tokens"}}}`
				body := sseTerminalBody(event, payload)
				var err error
				if passthrough {
					_, err = svc.handlePassthroughSSEToJSON(newNonStreamingSSEResponse(), c, newNonStreamingFailoverAccount(), body, "model", "model")
				} else {
					_, err = svc.handleSSEToJSON(newNonStreamingSSEResponse(), c, newNonStreamingFailoverAccount(), body, "model", "model")
				}
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover))
				require.Equal(t, 400, rec.Code)
				require.Equal(t, "context_length_exceeded", gjson.Get(rec.Body.String(), "error.code").String())
				require.Equal(t, 200, c.GetInt(OpsUpstreamStatusCodeKey), "retain the upstream HTTP envelope")
			})
		}
	}
}

func TestOpenAIContextLimitIgnoresEchoAndCredentialFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{502, `{"error":{"message":"Temporary outage"},"echo":"context_length_exceeded"}`},
		{401, `{"error":{"message":"context_length_exceeded"}}`},
		{429, `{"error":{"message":"context_length_exceeded"}}`},
	} {
		c, rec := newNonStreamingFailoverContext(t)
		svc := newNonStreamingFailoverService()
		require.False(t, svc.writeOpenAIContextLimitError(&http.Response{StatusCode: tc.status, Header: http.Header{}}, c, newNonStreamingFailoverAccount(), []byte(tc.body), false))
		require.False(t, c.Writer.Written())
		require.Empty(t, rec.Body.String())
	}
}

func TestNormalizeOpsNormalizedContextLimitKeepsRawStatus(t *testing.T) {
	for _, rawStatus := range []int{200, 502} {
		message := "context_length_exceeded: Too many tokens"
		entry := &OpsInsertErrorLogInput{StatusCode: 400, UpstreamStatusCode: &rawStatus,
			UpstreamErrorMessage: &message, ErrorOwner: "provider", ErrorPhase: "upstream"}
		NormalizeOpsClientRejection(entry)
		require.True(t, entry.IsBusinessLimited)
		require.Equal(t, "client", entry.ErrorOwner)
		require.Equal(t, "context_limit", entry.ErrorType)
		require.Equal(t, rawStatus, *entry.UpstreamStatusCode)
	}
	require.True(t, IsOpsContextLimitRejection(502, 502, "context_length_exceeded"), "explicit wrapped rejections also use the historical classification from PR666")
}

func TestOpenAIContextLimitCommittedCompactKeepsSSE(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()
	svc := newNonStreamingFailoverService()
	require.True(t, svc.writeOpenAIContextLimitError(newNonStreamingSSEResponse(), c,
		newNonStreamingFailoverAccount(), []byte(`{"error":{"code":"context_length_exceeded","message":"Too many tokens"}}`), false))
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, rec.Body.String(), "event: response.failed")
	require.Contains(t, rec.Body.String(), `"code":"context_length_exceeded"`)
	require.NotContains(t, rec.Body.String(), `"type":"invalid_request_error"`, "do not append a JSON envelope to an existing stream")
}
