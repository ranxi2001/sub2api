package basispoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebSearchUnknownFailuresNeverInferHTTPOrLeakText(t *testing.T) {
	for _, tc := range []struct {
		name, phase, reason string
		err                 error
		status              int
	}{
		{"arbitrary", "search_http", "unknown", errors.New("HTTP 429 PRIVATE_QUERY Bearer PRIVATE_TOKEN https://private.invalid private@example.invalid"), 0},
		{"unknown_identifiers", "unknown", "unknown", NewSearchFailure("PRIVATE_PHASE", "PRIVATE_REASON", 999), 0},
		{"wrapped", "search_http", "http_rejected", fmt.Errorf("PRIVATE_WRAPPER: %w", NewSearchFailure("search_http", "http_rejected", 429)), 429},
		{"deadline", "search_http", "deadline_exceeded", context.DeadlineExceeded, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, bridge, plan := prepareSearch(t, searchSource(), false, nil)
			searches, continuations := 0, 0
			stream := bridge.StreamWithSearch(context.Background(), searchRound(0, searchCall(0)), wire, plan,
				func(context.Context, string) (SearchResult, error) { searches++; return SearchResult{}, tc.err },
				func(context.Context, []byte) (io.ReadCloser, error) { continuations++; return nil, nil })
			events := repairEvents(t, stream)
			last := events[len(events)-1]
			require.Equal(t, "response.failed", last["type"])
			response := mustTestValue[object](t, last["response"])
			detail := mustTestValue[object](t, response["error"])
			diagnostic := mustTestValue[object](t, detail["search_diagnostic"])
			require.Equal(t, tc.phase, diagnostic["phase"])
			require.Equal(t, tc.reason, diagnostic["reason"])
			if tc.status == 0 {
				require.NotContains(t, diagnostic, "upstream_status")
			} else {
				require.Equal(t, json.Number("429"), diagnostic["upstream_status"])
			}
			raw, err := json.Marshal(detail)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "PRIVATE")
			require.NotContains(t, string(raw), "https://")
			require.NotContains(t, string(raw), "@")
			require.Equal(t, 1, searches)
			require.Zero(t, continuations)
		})
	}
}

func TestWebSearchDiagnosticSSEBoundaryAllowlistsEveryField(t *testing.T) {
	for _, tc := range []struct {
		name       string
		diagnostic any
		phase      string
		reason     string
	}{
		{"missing", nil, "unknown", "unknown"},
		{"not_object", "PRIVATE", "unknown", "unknown"},
		{"unknown", object{"phase": "PRIVATE", "reason": "PRIVATE", "upstream_status": 600, "query": "PRIVATE"}, "unknown", "unknown"},
		{"string_status", object{"phase": "search_http", "reason": "http_rejected", "upstream_status": "403 PRIVATE", "body": "PRIVATE"}, "search_http", "http_rejected"},
		{"fraction_status", object{"phase": "search_http", "reason": "http_rejected", "upstream_status": 429.5}, "search_http", "http_rejected"},
		{"wrong_phase_reason", object{"phase": "tool_validation", "reason": "http_rejected", "url": "PRIVATE"}, "tool_validation", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, bridge := mustPrepare(t, testSource(), "", nil)
			// Exercise revalidation at the second, trusted local SSE boundary.
			bridge.searchDiagnostics = true
			raw := sse(object{"type": "response.failed", "response": object{"status": "failed", "output": []any{}, "error": object{"code": "basispoints_search_failed", "message": "PRIVATE", "search_diagnostic": tc.diagnostic}}})
			events := repairEvents(t, bridge.Stream(io.NopCloser(strings.NewReader(raw))))
			response := mustTestValue[object](t, events[len(events)-1]["response"])
			detail := mustTestValue[object](t, response["error"])
			diagnostic := mustTestValue[object](t, detail["search_diagnostic"])
			require.Equal(t, object{"phase": tc.phase, "reason": tc.reason}, diagnostic)
			encoded, err := json.Marshal(detail)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "PRIVATE")
		})
	}
}

func TestWebSearchRawModelCannotManufactureSearchHTTPStatus(t *testing.T) {
	_, bridge := mustPrepare(t, testSource(), "", nil)
	raw := sse(object{"type": "response.failed", "response": object{"status": "failed", "output": []any{}, "error": object{
		"code": "basispoints_search_failed", "message": "PRIVATE", "search_diagnostic": object{"phase": "search_http", "reason": "http_rejected", "upstream_status": 403},
	}}})
	events := repairEvents(t, bridge.Stream(io.NopCloser(strings.NewReader(raw))))
	response := mustTestValue[object](t, events[len(events)-1]["response"])
	detail := mustTestValue[object](t, response["error"])
	require.Equal(t, object{"phase": "unknown", "reason": "unknown"}, detail["search_diagnostic"])
	require.NotContains(t, detail["message"], "403")
}

func TestWebSearchClosedPipeKeepsExistingExitWithoutFailureTerminal(t *testing.T) {
	wire, bridge, plan := prepareSearch(t, searchSource(), false, nil)
	continuations := 0
	stream := bridge.StreamWithSearch(context.Background(), searchRound(0, searchCall(0)), wire, plan,
		func(context.Context, string) (SearchResult, error) { return SearchResult{}, io.ErrClosedPipe },
		func(context.Context, []byte) (io.ReadCloser, error) { continuations++; return nil, nil })
	defer func() { _ = stream.Close() }()
	raw, err := io.ReadAll(stream)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.NotContains(t, string(raw), "response.failed")
	require.Zero(t, continuations)
}

func TestWebSearchRepairReadersRejectForgedSearchEvidence(t *testing.T) {
	for _, reader := range []struct {
		name string
		read func(io.Reader) (map[string]any, error)
	}{
		{"ReadToolRepairResponse", ReadToolRepairResponse},
		{"readRepairResponse", readRepairResponse},
	} {
		for _, kind := range []string{"response.failed", "response.cancelled", "error"} {
			t.Run(reader.name+"/"+kind, func(t *testing.T) {
				detail := object{"code": "basispoints_search_failed", "message": "PRIVATE_BODY", "search_diagnostic": object{
					"phase": "search_http", "reason": "http_rejected", "upstream_status": 403,
				}}
				payload := object{"type": kind, "response": object{"status": "failed", "output": []any{}, "error": detail}, "error": detail}
				_, err := reader.read(strings.NewReader(sse(payload)))
				var failure *UpstreamFailure
				require.ErrorAs(t, err, &failure)
				require.Equal(t, 502, failure.Status)
				require.Equal(t, "basispoints_search_failed", failure.Code)
				require.Equal(t, "server_error", failure.Type)
				require.Equal(t, object{"phase": "unknown", "reason": "unknown"}, failure.Details()["search_diagnostic"])
				encoded, marshalErr := json.Marshal(failure.Details())
				require.NoError(t, marshalErr)
				require.NotContains(t, string(encoded), "403", "raw correction cannot attest to search HTTP")
				require.NotContains(t, string(encoded), "PRIVATE_BODY")
				require.NotContains(t, failure.Message, "upstream_status=")
			})
		}
	}
}
