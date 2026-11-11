//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPriorityErrorRateWindowScopeAndPrior(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	now := time.Unix(1800000000, 0).Truncate(time.Minute)
	key := priorityAttemptKey{accountID: 1, model: "gpt-6.1-sol", endpoint: "/v1/responses"}
	stats.attempts.report(key, false, now)
	got := stats.attempts.snapshot(key, now, 10, 5)
	require.Equal(t, int64(1), got.Failures)
	require.Equal(t, 5, got.PriorSamples)
	require.Equal(t, now, got.ObservedAt, "read time must be the clock used for the window, not the report time")
	require.InDelta(t, 1.0/6, got.SmoothedRate, 1e-9)
	require.Equal(t, int64(0), stats.attempts.snapshot(priorityAttemptKey{accountID: 1, model: "other", endpoint: "/v1/responses"}, now, 10, 5).Failures)
	require.Equal(t, int64(0), stats.attempts.snapshot(priorityAttemptKey{accountID: 1, model: key.model, endpoint: "/v1/chat/completions"}, now, 10, 5).Failures)
	expired := stats.attempts.snapshot(key, now.Add(10*time.Minute), 10, 5)
	require.Nil(t, expired.ErrorRate, "expired evidence must stay unknown, never zero percent")
	require.Equal(t, 5, expired.PriorSamples)
	require.Equal(t, now.Add(10*time.Minute), expired.ObservedAt)
	require.Nil(t, newOpenAIAccountRuntimeStats().attempts.snapshot(key, now, 10, 5).ErrorRate, "runtime evidence is not cross-instance")
	for i := 0; i < 9; i++ {
		stats.attempts.report(key, true, now)
	}
	got = stats.attempts.snapshot(key, now, 10, 5)
	require.Equal(t, int64(9), got.Successes)
	require.InDelta(t, .1, *got.ErrorRate, 1e-9)
	require.InDelta(t, 1.0/15, got.SmoothedRate, 1e-9)
}

func TestPriorityErrorRateConcurrentBounded(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	now := time.Now()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				stats.attempts.report(priorityAttemptKey{accountID: 1, model: "model", endpoint: "/v1/responses"}, i%2 == 0, now)
			}
		}()
	}
	wg.Wait()
	got := stats.attempts.snapshot(priorityAttemptKey{accountID: 1, model: "model", endpoint: "/v1/responses"}, now, 10, 5)
	require.Equal(t, int64(400), got.Successes)
	require.Equal(t, int64(400), got.Failures)
	for i := 0; i < priorityAttemptMaxKeys+20; i++ {
		stats.attempts.report(priorityAttemptKey{accountID: int64(i + 2), model: "model", endpoint: "/v1/responses"}, false, now)
	}
	require.Len(t, stats.attempts.entries, priorityAttemptMaxKeys)
}

func TestPriorityErrorRateBodyOutcomes(t *testing.T) {
	guard := "Tool arguments stopped making progress: consecutive whitespace outside JSON strings (output_index=1, whitespace_bytes=1024, limit_bytes=1024)"
	cases := []struct {
		name             string
		status           int
		body             string
		stream           bool
		cancel           bool
		success, failure int64
	}{
		{"success", 200, `{"status":"completed","output":[]}`, false, false, 1, 0},
		{"stream success", 200, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", true, false, 1, 0},
		{"guard 502", 502, fmt.Sprintf(`{"error":{"type":"server_error","message":%q}}`, guard), false, false, 0, 1},
		{"guard after HTTP200", 200, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: " + fmt.Sprintf(`{"type":"response.failed","response":{"error":{"code":"server_error","message":%q}}}`, guard) + "\n\n", true, false, 0, 1},
		{"bare guard error", 200, "data: " + fmt.Sprintf(`{"type":"error","error":{"message":%q}}`, guard) + "\n\n", true, false, 0, 1},
		{"missing terminal", 200, "data: {\"type\":\"response.created\"}\n\n", true, false, 0, 1},
		{"cancelled", 200, "data: {\"type\":\"response.created\"}\n\n", true, true, 0, 0},
		{"invalid input", 400, `{"error":{"type":"invalid_request_error"}}`, false, false, 0, 0},
		{"bps inactive JSON", 502, `{"error":{"code":"user_inactive"}}`, true, false, 0, 0},
		{"bps quota JSON", 429, `{"error":{"code":"insufficient_quota"}}`, true, false, 0, 0},
		{"unrecognized JSON", 200, `{}`, false, false, 0, 0},
		{"inactive", 502, `{"error":{"code":"user_inactive"}}`, false, false, 0, 0},
		{"quota", 429, `{"error":{"code":"insufficient_quota"}}`, false, false, 0, 0},
		{"permission", 403, `{"error":{"code":"permission_denied"}}`, false, false, 0, 0},
		{"upstream rate limit", 429, `{"error":{"code":"rate_limit_exceeded"}}`, false, false, 0, 1},
		{"capacity HTTP400", 400, `{"error":{"type":"invalid_request_error","message":"Selected model is at capacity. Please try a different model."}}`, false, false, 0, 1},
		{"capacity HTTP200", 200, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Selected model is at capacity. Please try a different model.\"}}}\n\n", true, false, 0, 1},
		{"unknown oversized", 200, strings.Repeat("x", priorityAttemptInspectBytes+1), false, false, 0, 0},
		{"in progress JSON", 200, `{"status":"in_progress"}`, false, false, 0, 0},
		{"failed followed by done", 200, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\"}}}\n\ndata: [DONE]\n\n", true, false, 0, 1},
		{"capacity", 503, `{"error":{"code":"overloaded"}}`, false, false, 0, 1},
		{"app input rejection", 200, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"invalid_request_error\"}}}\n\n", true, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stats := newOpenAIAccountRuntimeStats()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			key := priorityAttemptKey{accountID: 1, model: "gpt-6.1-sol", endpoint: "/v1/responses"}
			b := newPriorityAttemptBody(io.NopCloser(strings.NewReader(tc.body)), stats, key, ctx, tc.status, tc.stream)
			_, err := io.ReadAll(b)
			require.NoError(t, err)
			if tc.cancel {
				cancel()
			}
			require.NoError(t, b.Close())
			require.NoError(t, b.Close())
			got := stats.attempts.snapshot(key, time.Now(), 10, 5)
			require.Equal(t, tc.success, got.Successes)
			require.Equal(t, tc.failure, got.Failures)
		})
	}
}
