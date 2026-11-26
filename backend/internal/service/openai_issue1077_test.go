package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIssue1077HandshakeAccountRejection(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		status       int
		wantFailover bool
	}{
		{"credential", `{"error":{"code":"invalid_api_key","type":"authentication_error"}}`, 401, true},
		{"quota", `{"error":{"type":"usage_limit_reached"}}`, 429, true},
		{"proxy", `{"error":{"code":"proxy_authentication_required","type":"authentication_error"}}`, 401, false},
		{"bare_401", `Unauthorized`, 401, false},
		{"short_throttle", `{"error":{"code":"rate_limit_exceeded"}}`, 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			conn := &openAIWSCaptureConn{}
			d := &wsSSETestDialer{conn: conn, status: tc.status, err: &openAIWSHandshakeError{Body: []byte(tc.body), Err: errors.New("fixture handshake refusal")}}
			h := &httpUpstreamRecorder{}
			s := wsSSETestService(t, d, h)
			_, err := s.Forward(context.Background(), c, wsSSETestAccount(), []byte(wsSSETestRequest))
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.Equal(t, tc.wantFailover, errors.As(err, &failover), "ISSUE1077_HANDSHAKE_ACCOUNT_REJECTION: %v", err)
			if tc.wantFailover {
				require.Equal(t, tc.status, failover.StatusCode)
				require.False(t, failover.RetryableOnSameAccount)
				require.False(t, c.Writer.Written(), "handler must own the same-POST retry")
			}
			require.Empty(t, h.requests, "account rejection must not fall back to the same account over HTTP")
			require.EqualValues(t, 1, d.calls.Load())
			conn.mu.Lock()
			writes := len(conn.writes)
			conn.mu.Unlock()
			require.Zero(t, writes, "even a prewarm must not be sent")
		})
	}
}
