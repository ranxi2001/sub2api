package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIssue1077HandshakeAccountRejection(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		status       int
		wantFailover bool
		headers      http.Header
	}{
		{"credential", `{"error":{"code":"invalid_api_key","type":"authentication_error"}}`, 401, true, nil},
		{"quota", `{"error":{"type":"usage_limit_reached"}}`, 429, true, nil},
		{"proxy", `{"error":{"code":"proxy_authentication_required","type":"authentication_error"}}`, 401, false, nil},
		{"bare_401", `Unauthorized`, 401, false, nil},
		{"generic_auth", `{"error":{"type":"authentication_error","message":"relay rejected"}}`, 401, false, nil},
		{"unknown_auth", `{"error":{"code":"relay_rejected","type":"authentication_error"}}`, 401, false, nil},
		{"proxy_header", `{"error":{"code":"invalid_api_key"}}`, 401, false, http.Header{"Proxy-Authenticate": {"fixture"}}},
		{"conflict_quota", `{"error":{"code":"rate_limit_exceeded","type":"usage_limit_reached"}}`, 429, false, nil},
		{"proxy_quota", `{"error":{"code":"proxy_authentication_required","type":"usage_limit_reached"}}`, 429, false, nil},
		{"duplicate", `{"error":{"code":"usage_limit_reached","code":"rate_limit_exceeded"}}`, 429, false, nil},
		{"message_only", `{"error":{"message":"usage_limit_reached"}}`, 429, false, nil},
		{"retry_after", `{"error":{"type":"usage_limit_reached"}}`, 429, false, http.Header{"Retry-After": {"1"}}},
		{"malformed_code", `{"error":{"code":[],"type":"usage_limit_reached"}}`, 429, false, nil},
		{"empty_code", `{"error":{"code":"","type":"usage_limit_reached"}}`, 429, false, nil},
		{"short_throttle", `{"error":{"code":"rate_limit_exceeded"}}`, 429, false, nil},
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
			s.openaiWSPool.setClientDialerForTest(&wsSSEGuardHeaderDialer{d, tc.headers})
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

func TestIssue1077HandshakeOriginalPayloadSafety(t *testing.T) {
	for _, extra := range []string{
		`,"tools":[{"type":"file_search","vector_store_ids":["vs_private"]}]`,
		`,"tools":[{"type":"code_interpreter","container":"private"}]`,
		`,"conversation":"conv_private"`,
		`,"previous_response_id":"resp_private"`,
		`,"input":[{"type":"function_call_output","call_id":"orphan","output":"done"}]`,
		`,"input":[{"type":"input_image","image_url":"fixture"}]`,
		`,"input":[{"type":"reasoning","encrypted_content":"fixture"}]`,
	} {
		t.Run(extra, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			d := &wsSSETestDialer{status: 401, err: &openAIWSHandshakeError{Body: []byte(`{"error":{"code":"invalid_api_key"}}`), Err: errors.New("fixture refusal")}}
			s := wsSSETestService(t, d, wsSSEGuardHTTPUpstream())
			base := `{"model":"gpt-5.1","stream":true,"instructions":"test"`
			if !strings.HasPrefix(extra, `,"input":`) {
				base += `,"input":"hello"`
			}
			body := []byte(base + extra + "}")
			original := string(body)
			_, err := s.Forward(context.Background(), c, wsSSETestAccount(), body)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover), "bound original payload cannot open handshake failover")
			require.Equal(t, original, string(body))
		})
	}
}

func TestIssue1077StreamQuotaHasOneSafeTerminal(t *testing.T) {
	for _, before := range []string{"", `{"type":"response.created","sequence_number":12,"response":{"id":"resp_quota"}}`, `{"type":"response.output_text.delta","delta":"partial"}`} {
		for _, failure := range []string{
			`{"type":"error","status_code":429,"error":{"type":"usage_limit_reached","message":"private fixture upstream body","resets_at":1795726241}}`,
			`{"type":"response.failed","response":{"error":{"code":"usage_limit_reached","message":"private fixture upstream body"}}}`,
		} {
			t.Run(before+failure[:20], func(t *testing.T) {
				events := []string{}
				if before != "" {
					events = append(events, before)
				}
				events = append(events, failure)
				w, c, result, _, err := issue776Forward(t, events, nil)
				require.Error(t, err)
				require.Nil(t, result, "quota must not manufacture billable usage")
				var replay *UpstreamFailoverError
				require.False(t, errors.As(err, &replay), "generation already sent; never switch on absence of tokens")
				require.Equal(t, http.StatusOK, w.Code)
				messages := issue776SSEMessages(t, w.Body.String())
				want := 1
				if before != "" {
					want++
				}
				require.Len(t, messages, want)
				require.NotContains(t, w.Body.String(), "private fixture")
				require.NotContains(t, w.Body.String(), `"type":"error"`)
				require.Contains(t, w.Body.String(), `"code":"usage_limit_reached"`)
				marked, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.Equal(t, 429, marked.IntendedStatus)
				require.NotNil(t, marked.UpstreamWSFailure)
				require.Equal(t, "upstream_event", marked.UpstreamWSFailure.Phase)
			})
		}
	}
}

func TestIssue1077StreamQuotaPreservesObservedUsage(t *testing.T) {
	_, _, result, _, err := issue776Forward(t, []string{
		`{"type":"response.output_text.delta","delta":"partial"}`,
		`{"type":"response.failed","response":{"id":"resp_used","error":{"code":"usage_limit_reached"},"usage":{"input_tokens":7,"output_tokens":2}}}`,
	}, nil)
	require.Error(t, err)
	require.NotNil(t, result, "authoritative partial usage must survive terminal conversion")
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}
