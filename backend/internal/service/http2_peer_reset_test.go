package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestHTTP2PeerResetSafeClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		peer   bool
		reason string
	}{
		{"stdlib wrapped", fmt.Errorf("reader: %w", errors.New("stream error: stream ID 123; INTERNAL_ERROR; received from peer")), true, "INTERNAL_ERROR"},
		{"typed", http2.StreamError{StreamID: 999, Code: http2.ErrCodeCancel, Cause: errors.New("received from peer")}, true, "CANCEL"},
		{"unknown reason", errors.New("stream error: stream ID 6; customer-secret; received from peer"), true, ""},
		{"arbitrary http2", errors.New("http2: stream diagnostic customer-secret"), false, ""},
		{"unrelated internal error", errors.New("INTERNAL_ERROR customer-secret"), false, ""},
		{"local cause", http2.StreamError{StreamID: 999, Code: http2.ErrCodeInternal, Cause: errors.New("customer-secret")}, false, ""},
		{"cancel", fmt.Errorf("stream error: stream ID 6; INTERNAL_ERROR; received from peer: %w", context.Canceled), false, ""},
		{"deadline", context.DeadlineExceeded, false, ""},
		{"clean truncation", ErrOpenAIUpstreamStreamTruncated, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, message := classifyOpenAIUpstreamStreamReadError(tc.err)
			require.NotContains(t, message, "customer-secret")
			require.NotContains(t, message, "stream ID")
			if tc.peer {
				require.Contains(t, message, "reset by peer")
				require.Contains(t, message, "Do not automatically retry")
				if tc.reason != "" {
					require.Contains(t, message, tc.reason)
				}
			} else {
				require.NotContains(t, message, "reset by peer")
			}
		})
	}
}

func TestHTTP2PeerResetDoesNotTriggerHTTPHealthBreaker(t *testing.T) {
	reset := newOpenAIUpstreamStreamReadError(errors.New("stream error: stream ID 3; INTERNAL_ERROR; received from peer"))
	status, _, eligible := classifyOpenAIAPIKeyHealthFailure(reset)
	require.False(t, eligible)
	require.Zero(t, status)
	status, _, eligible = classifyOpenAIAPIKeyHealthFailure(&UpstreamFailoverError{StatusCode: http.StatusBadGateway})
	require.True(t, eligible, "genuine HTTP 502 remains eligible")
	require.Equal(t, 502, status)
}

func TestHTTP2PeerResetConcurrencyDoesNotInventCooldown(t *testing.T) {
	now := time.Now()
	cfg := DefaultOAuthAutoConfig()
	cfg.Revision = "synthetic"
	cfg.SuccessesPerStep = 4
	cfg.MaxConcurrency = 10
	cfg.UpgradeStep = 1
	cfg.CooldownSeconds = 60
	state := AutoConfigConcurrencyState{Revision: cfg.Revision, Concurrency: 2, Successes: 3}
	var event AccountConcurrencyResult
	require.NoError(t, json.Unmarshal([]byte(`{"AccountID":7,"Success":false,"FailureCategory":"transport_error"}`), &event))
	next, concurrency := AdvanceConcurrency(state, 2, cfg, event, now)
	require.Zero(t, next.Successes, "transport interruption must not count as success")
	require.Equal(t, 2, concurrency)
	require.True(t, next.PausedUntil.IsZero(), "transport reset does not prove an account capacity cooldown")
}

func TestHTTP2PeerResetUserDetailsDiscardArbitraryDiagnostics(t *testing.T) {
	status := 502
	detail := ToUserErrorRequestDetail(&OpsErrorLogDetail{OpsErrorLog: OpsErrorLog{Type: "transport_error", Phase: "network", StatusCode: 200, Message: "stream ID 123 customer-secret"}, UpstreamStatusCode: &status, ErrorBody: `{"error":{"reset_reason":"customer-secret","cause":"private","headers":{"Authorization":"secret"}}}`})
	data, err := json.Marshal(detail)
	require.NoError(t, err)
	require.Nil(t, detail.UpstreamStatusCode)
	require.Contains(t, string(data), "transport_error")
	for _, forbidden := range []string{"stream ID", "customer-secret", "Authorization", "private", "502"} {
		require.NotContains(t, string(data), forbidden)
	}
}
