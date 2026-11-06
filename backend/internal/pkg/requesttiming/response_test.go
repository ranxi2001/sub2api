package requesttiming

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/transportdiag"
	"github.com/stretchr/testify/require"
)

func TestResponseAttemptSeparatesWebsocketContentAndFlush(t *testing.T) {
	start := time.Unix(100, 0)
	now := start.Add(50 * time.Millisecond)
	c := newCollector(start, 0, func() time.Time { return now })
	base := With(context.Background(), c)
	route := transportdiag.Route{Upstream: "websocket", Mode: "ctx_pool", Reason: "oauth_http_sse_acceleration", ResponseFormat: "sse"}
	ctx, tr := StartResponseAttempt(base, 17, 3, route)

	now = start.Add(100 * time.Millisecond)
	Content(ctx, []byte(`{"type":"response.created"}`))
	Output(ctx, false, false, "")
	SSEEventFlushed(ctx)
	now = start.Add(300 * time.Millisecond)
	Content(ctx, []byte(`{"type":"response.reasoning_text.delta","delta":"synthetic"}`))
	Output(ctx, true, true, "")
	now = start.Add(800 * time.Millisecond)
	Content(ctx, []byte(`{"type":"response.output_text.delta","delta":"OK"}`))
	Output(ctx, true, true, "")
	now = start.Add(900 * time.Millisecond)
	tr.FinishResponseAttempt(nil, "completed")
	now = start.Add(time.Second)
	tr.FinishResponseAttempt(errors.New("late cleanup"), "failed")
	Stage(ctx, "late_after_end")
	Output(ctx, false, false, "failed")
	c.Finish(http.StatusOK, false)
	c.WhenFinished(func(s Snapshot) {
		require.Equal(t, 1, s.FinalResponseAttempt)
		require.Len(t, s.Attempts, 1)
		a := s.Attempts[0]
		require.Equal(t, route, a.Route)
		require.Equal(t, "egress", a.Kind)
		require.Equal(t, "completed", a.Terminal)
		require.Empty(t, a.Error)
		require.Equal(t, 900.0, *a.EndMS, "finish is idempotent")
		require.NotContains(t, a.Events, "response_headers")
		require.NotContains(t, a.Events, "first_byte")
		require.NotContains(t, a.Events, "late_after_end")
		v := DeriveContentObservation(s)
		require.Equal(t, "native_websocket_events", v.Source)
		require.Equal(t, "final_websocket_egress_attempt_offsets_from_application_entry", v.Scope)
		require.Equal(t, 100.0, *v.SSEEventFirstMS)
		require.Equal(t, 300.0, *v.SemanticFirstMS)
		require.Equal(t, 300.0, *v.ReasoningFirstMS)
		require.Equal(t, 800.0, *v.VisibleTextFirstMS)
		require.Nil(t, v.ResponseByteFirstMS)
	})
}

func TestResponseAttemptProjectionIgnoresHandshakeAndUsesHTTPFallback(t *testing.T) {
	start := time.Unix(100, 0)
	now := start
	c := newCollector(start, 0, func() time.Time { return now })
	base := With(context.Background(), c)
	ctx, ws := StartResponseAttempt(base, 1, 0, transportdiag.Route{Upstream: "websocket", Mode: "ctx_pool"})
	// An auxiliary HTTP child cannot become the final response, even if it has
	// network events and is appended after the native response's root.
	req := httptest.NewRequest(http.MethodGet, "https://example.test/responses", nil).WithContext(ctx)
	req, handshake := StartAttempt(req, 1, 0)
	req, physical := StartTransport(req)
	now = start.Add(10 * time.Millisecond)
	Stage(req.Context(), "first_byte")
	physical.Response(nil, nil)
	handshake.Response(nil, nil)
	now = start.Add(20 * time.Millisecond)
	ws.FinishResponseAttempt(errors.New("handshake rejected"), "")
	// The next request starts outside the finished WS context, exactly as the
	// caller's safe HTTP fallback does.
	req = httptest.NewRequest(http.MethodPost, "https://example.test/responses", nil).WithContext(base)
	req, httpRoot := StartAttempt(req, 1, 0)
	req, httpPhysical := StartTransport(req)
	now = start.Add(40 * time.Millisecond)
	Stage(req.Context(), "first_byte")
	now = start.Add(70 * time.Millisecond)
	Output(req.Context(), true, true, "")
	Content(req.Context(), []byte(`{"type":"response.output_text.delta","delta":"OK"}`))
	SSEEventFlushed(req.Context())
	now = start.Add(80 * time.Millisecond)
	httpPhysical.Response(nil, nil)
	httpRoot.Response(nil, nil)
	c.Finish(http.StatusOK, false)
	c.WhenFinished(func(s Snapshot) {
		require.Equal(t, 4, s.FinalResponseAttempt)
		require.Equal(t, 1, s.Attempts[1].Parent)
		require.Equal(t, 2, s.Attempts[2].Parent)
		v := DeriveContentObservation(s)
		require.Equal(t, "httptrace_and_typed_sse", v.Source)
		require.Equal(t, 40.0, *v.ResponseByteFirstMS)
		require.Equal(t, 70.0, *v.SSEEventFirstMS)
		require.Equal(t, 70.0, *v.VisibleTextFirstMS)
		// A handshake appended after the WS root is not the native response.
		wsOnly := s
		wsOnly.FinalResponseAttempt = 1
		wsOnly.Attempts = s.Attempts[:3]
		v = DeriveContentObservation(wsOnly)
		require.Equal(t, "native_websocket_events", v.Source)
		require.Nil(t, v.ResponseByteFirstMS)
		require.Nil(t, v.SemanticFirstMS)
		require.Nil(t, v.SSEEventFirstMS)
	})
}

func TestResponseAttemptFinalScopeRejectsEarlierContentAndLateEvents(t *testing.T) {
	end := 1000.0
	old := Attempt{Kind: "egress", Number: 1, StartMS: 0, EndMS: &end, Route: transportdiag.Route{Upstream: "websocket", Mode: "ctx_pool"}, Events: map[string]float64{"first_semantic": 100, "first_sse_event_flush": 110}}
	last := old
	last.Number, last.StartMS, last.Events = 2, 900, map[string]float64{"first_visible_text": 1100}
	s := Snapshot{TotalMS: 1200, FinalResponseAttempt: 2, Attempts: []Attempt{old, last}}
	v := DeriveContentObservation(s)
	require.Nil(t, v.SemanticFirstMS)
	require.Nil(t, v.SSEEventFirstMS)
	require.Nil(t, v.VisibleTextFirstMS, "logical WS end bounds all WS events")
	s.FinalResponseAttempt = 3
	require.Equal(t, "not_collected", DeriveContentObservation(s).Status)
	s.FinalResponseAttempt = 2
	s.Truncated = true
	require.Equal(t, "not_collected", DeriveContentObservation(s).Status)
}

func TestResponseAttemptNoCollectorAndFinishedCollector(t *testing.T) {
	base := context.Background()
	ctx, tr := StartResponseAttempt(base, 1, 0, transportdiag.Route{})
	require.Equal(t, base, ctx)
	require.Nil(t, tr)
	tr.FinishResponseAttempt(nil, "completed")
	c := New(time.Now(), 0)
	c.Finish(http.StatusOK, false)
	ctx, tr = StartResponseAttempt(With(base, c), 1, 0, transportdiag.Route{})
	require.Nil(t, tr)
	_ = ctx
}
