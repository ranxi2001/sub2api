package requesttiming

import (
	"context"
	"math"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/transportdiag"
	"github.com/stretchr/testify/require"
)

func TestIssue605TypedContentDoesNotInferReasoning(t *testing.T) {
	for _, tc := range []struct {
		data, event  string
		reason, text bool
	}{
		{`{"type":"response.output_item.added","item":{"type":"message"}}`, "", false, false},
		{`{"type":"response.output_item.added","item":{"type":"function_call"}}`, "", false, false},
		{`{"type":"response.output_item.done","item":{"type":"reasoning"}}`, "", true, false},
		{`{"type":"response.reasoning_summary_text.delta","delta":"summary"}`, "", true, false},
		{`{"type":"response.output_text.delta","delta":"visible"}`, "", false, true},
		{`{"delta":"visible"}`, "response.output_text.delta", false, true},
		{`{"delta":"summary"}`, "response.reasoning_summary_text.delta", true, false},
		{`{"type":"response.created"}`, "response.output_text.delta", false, false},
	} {
		t.Run(tc.data+tc.event, func(t *testing.T) {
			c := New(time.Now(), 0)
			r := httptest.NewRequest("POST", "/", nil).WithContext(With(context.Background(), c))
			r, _ = StartAttempt(r, 1, 0)
			r, tr := StartTransport(r)
			Content(r.Context(), []byte(tc.data), tc.event)
			tr.Response(nil, nil)
			c.Finish(200, false)
			c.WhenFinished(func(s Snapshot) {
				_, reason := s.Attempts[1].Events["first_reasoning"]
				_, text := s.Attempts[1].Events["first_visible_text"]
				require.Equal(t, tc.reason, reason)
				require.Equal(t, tc.text, text)
				v := DeriveContentObservation(s)
				require.Equal(t, tc.reason, v.ReasoningFirstMS != nil)
				require.Equal(t, tc.text, v.VisibleTextFirstMS != nil)
				require.Equal(t, "unknown", v.ReasoningStreamingCapability)
				require.Equal(t, "none", v.CapabilitySource)
				require.Nil(t, v.ClientSocket.MinRTTMS)
				require.Nil(t, v.UpstreamSocket.Retransmissions)
			})
		})
	}
}

func TestPluginContentProjectionKeepsFinalAttemptScope(t *testing.T) {
	end := 1000.0
	plugin := Attempt{Kind: "egress", StartMS: 50, EndMS: &end, Route: transportdiag.Route{Mode: "plugin", Upstream: "plugin"}, Events: map[string]float64{"first_sse_event_flush": 100, "first_semantic": 300, "first_reasoning": 300, "first_visible_text": 800, "first_byte": 80}}
	s := Snapshot{TotalMS: 1000, Attempts: []Attempt{plugin}}
	v := DeriveContentObservation(s)
	require.Equal(t, "observed", v.Status)
	require.Equal(t, "host_plugin_body_parser", v.Source)
	require.Equal(t, "plugin_transport_not_observed", v.UpstreamSocket.Status)
	require.Equal(t, 100.0, *v.SSEEventFirstMS)
	require.Equal(t, 300.0, *v.SemanticFirstMS)
	require.Equal(t, 800.0, *v.VisibleTextFirstMS)
	require.Nil(t, v.ResponseByteFirstMS, "gRPC headers are not upstream socket evidence")
	for _, bad := range []float64{-1, 49, 1001, math.NaN(), math.Inf(1)} {
		plugin.Events["first_sse_event_flush"] = bad
		require.Nil(t, DeriveContentObservation(s).SSEEventFirstMS)
	}
	s.Events = map[string]float64{"first_sse_event_flush": 10, "first_semantic": 10}
	s.Attempts = append(s.Attempts, Attempt{Kind: "egress", StartMS: 900, EndMS: &end, Route: plugin.Route})
	v = DeriveContentObservation(s)
	require.Nil(t, v.SSEEventFirstMS, "do not borrow evidence from an earlier attempt or global events")
	require.Nil(t, v.SemanticFirstMS)
	s.Truncated = true
	require.Equal(t, "not_collected", DeriveContentObservation(s).Status)
}

func TestIssue605HistoricalAndProxySocketEvidenceStaysUnknown(t *testing.T) {
	v := DeriveContentObservation(Snapshot{Events: map[string]float64{"first_semantic": 10, "first_visible": 20}})
	require.Nil(t, v.ReasoningFirstMS)
	require.Nil(t, v.VisibleTextFirstMS)
	end := 100.0
	v = DeriveContentObservation(Snapshot{Attempts: []Attempt{{Kind: "http", StartMS: 0, EndMS: &end, ProxyID: 8, Events: map[string]float64{"first_byte": 5}}}})
	require.Equal(t, 5.0, *v.ResponseByteFirstMS)
	require.Equal(t, "proxy_obscured", v.UpstreamSocket.Status)
	require.Nil(t, v.UpstreamSocket.MinRTTMS)
}

func TestIssue605ContentAfterTransportEOFKeepsParserObservation(t *testing.T) {
	end := 100.0
	s := Snapshot{TotalMS: 300, Attempts: []Attempt{{Kind: "http", StartMS: 0, EndMS: &end, Events: map[string]float64{"headers_written": 10, "request_written": 20, "first_byte": 30, "first_reasoning": 150, "first_visible_text": 200, "first_semantic": 150, "first_visible": 200}}}}
	v := DeriveContentObservation(s)
	require.NotNil(t, v.ReasoningFirstMS, "buffered SSE may be parsed after transport EOF")
	require.Equal(t, 150.0, *v.ReasoningFirstMS, "buffered SSE may be parsed after transport EOF")
	require.Equal(t, 200.0, *v.VisibleTextFirstMS)
	require.Equal(t, 130.0, *DeriveLatency(s).Attempts[0].SemanticWait.MS)
	s.Attempts[0].Events["first_reasoning"] = 400
	require.Nil(t, DeriveContentObservation(s).ReasoningFirstMS, "request completion still bounds parser events")
}
