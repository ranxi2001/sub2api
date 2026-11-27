package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"github.com/stretchr/testify/require"
)

func TestAccountHTTPContextRejectionsDoNotPenalizeProvider(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		failures int64
	}{
		{"context_400", 400, `{"error":{"code":"context_length_exceeded","message":"too many tokens"}}`, 0},
		{"prompt_400", 400, `{"error":{"message":"prompt is too long: 300000 > 200000 tokens"}}`, 0},
		{"payload_413", 413, `request too large`, 0},
		{"context_422", 422, `{"error":{"message":"maximum context length exceeded"}}`, 0},
		{"wrapped_context_502", 502, `{"error":{"code":"context_length_exceeded"}}`, 0},
		{"wrapped_message_502", 502, `{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}`, 0},
		{"echoed_message_502", 502, `{"error":{"message":"temporary outage"},"input":"Your input exceeds the context window of this model. Please adjust your input and try again."}`, 1},
		{"conflicting_code_502", 502, `{"error":{"code":"authentication_error","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}`, 1},
		{"echo_is_not_an_error", 400, `{"error":{"message":"provider malfunction"},"input":"context_length_exceeded"}`, 1},
		{"auth_401", 401, `{"error":{"code":"context_length_exceeded"}}`, 1},
		{"permission_403", 403, `{"error":{"code":"context_length_exceeded"}}`, 1},
		{"capacity_429", 429, `{"error":{"code":"context_length_exceeded"}}`, 1},
		{"outage_502", 502, `{"error":{"message":"upstream unavailable"}}`, 1},
		{"other_400", 400, `{"error":{"message":"upstream integration failed"}}`, 1},
		{"malformed_400", 400, `{"error":{"code":"context_length_exceeded"}`, 1},
		{"oversized_400", 400, `{"error":{"code":"context_length_exceeded","message":"` + strings.Repeat("x", 65536) + `"}}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := upstreamstats.Default
			upstreamstats.Default = upstreamstats.NewStore(100, time.Now())
			t.Cleanup(func() { upstreamstats.Default = original })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			since := time.Now()
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/v1/responses", nil)
			require.NoError(t, err)
			resp, err := NewHTTPUpstream(nil).Do(req, "", 90007, 1)
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, tc.status, resp.StatusCode)
			require.Equal(t, tc.body, string(body), "the observer must preserve the caller's response")
			got := upstreamstats.Default.Snapshot([]int64{90007}, since, time.Now())[90007]
			require.Equal(t, tc.failures, got.Failures)
			require.Equal(t, tc.failures, got.Total, "client rejections must not dilute the denominator either")
		})
	}
}

func TestAccountHTTPUnreadErrorRemainsProviderEvidence(t *testing.T) {
	now := time.Now()
	store := upstreamstats.NewStore(10, now)
	body := &accountErrorResponseBody{ReadCloser: io.NopCloser(strings.NewReader(`{"error":{"code":"context_length_exceeded"}}`)),
		ctx: context.Background(), account: 9, status: 502, received: now, store: store}
	require.NoError(t, body.Close())
	require.NoError(t, body.Close())
	evidence := store.Snapshot([]int64{9}, now, time.Now())[9]
	require.Equal(t, int64(1), evidence.Failures, "no semantic proof without reading; close must not double count")
}
