package service

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercise both legal Reader termination shapes without relying on ReadAll's
// internal buffer size or a real network connection.
type prioritySSEEndingReader struct {
	payload  string
	ending   error
	withData bool
	closes   int
}

func (r *prioritySSEEndingReader) Read(p []byte) (int, error) {
	if r.payload == "" {
		return 0, r.ending
	}
	n := copy(p, r.payload)
	r.payload = r.payload[n:]
	if r.payload == "" && r.withData {
		return n, r.ending
	}
	return n, nil
}
func (r *prioritySSEEndingReader) Close() error { r.closes++; return nil }

func readPrioritySSEEvidence(t *testing.T, wire string, ending error, withData bool, status int, stream bool, endpoint string) PriorityErrorEvidence {
	t.Helper()
	origin := time.Unix(1800000000, 0)
	now := origin
	stats := newOpenAIAccountRuntimeStats()
	stats.now = func() time.Time { return now }
	key := priorityAttemptKey{accountID: 823, model: "synthetic-model", endpoint: endpoint}
	reader := &prioritySSEEndingReader{payload: wire, ending: ending, withData: withData}
	body := newPriorityAttemptBody(reader, stats, key, context.Background(), status, stream)
	now = origin.Add(100 * time.Millisecond)
	actual, err := io.ReadAll(body)
	if ending == io.EOF {
		require.NoError(t, err)
	} else {
		require.ErrorIs(t, err, ending)
	}
	require.Equal(t, wire, string(actual), "observation must preserve all body bytes")
	now = origin.Add(900 * time.Millisecond)
	require.NoError(t, body.Close())
	require.NoError(t, body.Close())
	require.Equal(t, 1, reader.closes, "settlement and underlying Close must be idempotent")
	require.Empty(t, body.buf)
	require.Empty(t, body.metricEvent.data, "cleanup must release unfinished payload")
	return stats.attempts.snapshot(key, now, 10, 5)
}

func TestPriorityResponsesIncompleteSSEExcluded(t *testing.T) {
	for _, payload := range []struct{ name, wire string }{
		{"partial", `data: {"type":"response.output_text.delta","delta":`},
		{"partial_then_header", "data: {\"delta\":\nevent: response.created"},
	} {
		for _, newline := range []bool{false, true} {
			for _, ending := range []struct {
				name string
				err  error
			}{{"EOF", io.EOF}, {"UnexpectedEOF", io.ErrUnexpectedEOF}} {
				for _, withData := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/newline=%t/%s/with_data=%t", payload.name, newline, ending.name, withData), func(t *testing.T) {
						wire := payload.wire
						if newline {
							wire += "\n"
						}
						got := readPrioritySSEEvidence(t, wire, ending.err, withData, 200, true, "/v1/responses")
						require.EqualValues(t, 0, got.Successes)
						require.EqualValues(t, 0, got.Failures, "unfinished JSON is unknown evidence, never an account failure")
						require.EqualValues(t, 0, got.LatencySamples)
						require.Nil(t, got.ErrorRate, "excluded evidence must remain unknown")
					})
				}
			}
		}
	}
}

func TestPriorityResponsesCompleteSSEEvidence(t *testing.T) {
	terminal := "data: " + `{"type":"response.completed","response":{"status":"completed"}}` + "\n\n"
	for _, prefix := range []struct{ name, wire string }{
		{"event_header", "event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n"},
		{"multiline", "event: response.output_text.delta\ndata: {\ndata: \"delta\":\"hello\"}\n\n"},
	} {
		for _, withData := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/with_data=%t", prefix.name, withData), func(t *testing.T) {
				got := readPrioritySSEEvidence(t, prefix.wire+terminal, io.EOF, withData, 200, true, "/v1/responses")
				require.EqualValues(t, 1, got.Successes)
				require.EqualValues(t, 0, got.Failures)
				require.EqualValues(t, 1, got.LatencySamples, "a valid key must record the semantic latency sample")
				require.Equal(t, 100.0, got.LatencyTotalMS)
			})
		}
	}
}

func TestPriorityResponsesIncompleteSSEPreservesOtherOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, wire, endpoint string
		status               int
		stream               bool
		success, failure     int64
	}{
		{"explicit_failed", "data: " + `{"type":"response.failed","response":{"error":{"code":"server_error"}}}` + "\n\n" + `data: {"delta":`, "/v1/responses", 200, true, 0, 1},
		{"HTTP_failure", `data: {"delta":`, "/v1/responses", 503, true, 0, 1},
		{"nonstream_success", `{"status":"completed","output":[]}`, "/v1/responses", 200, false, 1, 0},
		{"nonstream_unknown", `{"status":`, "/v1/responses", 200, false, 0, 0},
		{"valid_missing_terminal", "data: " + `{"type":"response.created"}` + "\n\n", "/v1/responses", 200, true, 0, 1},
		{"chat_invalid", "data: " + `{"choices":` + "\n", "/v1/chat/completions", 200, true, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readPrioritySSEEvidence(t, tc.wire, io.EOF, false, tc.status, tc.stream, tc.endpoint)
			require.Equal(t, tc.success, got.Successes)
			require.Equal(t, tc.failure, got.Failures)
			require.EqualValues(t, 0, got.LatencySamples)
		})
	}
}

func TestOpenAISSEMetricEventStateRecovery(t *testing.T) {
	for _, boundary := range []string{"", "event", "event: response.output_text.delta"} {
		for _, prefix := range []struct {
			name, line string
			discard    bool
		}{
			{"incomplete", `data: {"delta":`, false},
			{"malformed", `data: {"delta":]`, false},
			{"overflow", "data: " + strings.Repeat("x", 65), true},
		} {
			t.Run(fmt.Sprintf("%s/boundary=%q", prefix.name, boundary), func(t *testing.T) {
				event := openAISSEMetricEvent{limit: 64}
				data, _ := event.observeLine("event: response.created")
				require.Nil(t, data)
				data, _ = event.observeLine(prefix.line)
				require.Nil(t, data)
				require.Equal(t, prefix.discard, event.discard)
				require.LessOrEqual(t, len(event.data), 64, "partial payload must stay bounded")
				event.observeLine(boundary)
				require.Empty(t, event.data)
				require.False(t, event.discard)
				data, typ := event.observeLine(`data: {"delta":"hello"}`)
				require.JSONEq(t, `{"delta":"hello"}`, string(data))
				if boundary == "event: response.output_text.delta" {
					require.Equal(t, "response.output_text.delta", typ)
				} else {
					require.Empty(t, typ, "reset must not inherit the old event type")
				}
				event.observeLine("")
				event.observeLine("event: response.created")
				data, typ = event.observeLine(`data: {"type":"response.output_text.delta","delta":"hello"}`)
				require.NotNil(t, data)
				require.Equal(t, "response.output_text.delta", typ, "payload type must retain precedence after recovery")
			})
		}
	}
	t.Run("overflow_discards_until_boundary", func(t *testing.T) {
		event := openAISSEMetricEvent{limit: 32}
		event.observeLine("data: " + strings.Repeat("x", 33))
		data, _ := event.observeLine(`data: {"delta":"ignored"}`)
		require.Nil(t, data)
		require.Empty(t, event.data)
		require.True(t, event.discard)
		event.observeLine("event: response.output_text.delta")
		data, typ := event.observeLine("data: {")
		require.Nil(t, data)
		require.Empty(t, typ, "incomplete JSON must not emit an event type")
		data, typ = event.observeLine(`data: "delta":"ok"}`)
		require.JSONEq(t, `{"delta":"ok"}`, string(data))
		require.Equal(t, "response.output_text.delta", typ)
	})
}
