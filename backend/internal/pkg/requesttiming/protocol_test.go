package requesttiming

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSmartProtocolTimingSnapshotCompatibility(t *testing.T) {
	c := New(time.Now(), 0)
	ctx := With(context.Background(), c)
	SetProtocolDecision(ctx, ProtocolDecision{Ingress: "ws", Upstream: "http", Candidate: "http", Mode: "auto", PredictionSource: "heuristic_cap"})
	SetProtocolDecision(ctx, ProtocolDecision{Upstream: "ws"})
	var first, second Snapshot
	c.WhenFinished(func(s Snapshot) { first = s })
	c.Finish(200, false)
	require.Equal(t, "http", first.ProtocolDecision.Upstream)
	first.ProtocolDecision.Upstream = "mutated"
	c.WhenFinished(func(s Snapshot) { second = s })
	require.Equal(t, "http", second.ProtocolDecision.Upstream)
	var legacy Snapshot
	require.NoError(t, json.Unmarshal([]byte(`{"version":1}`), &legacy))
	require.Nil(t, legacy.ProtocolDecision)
	raw, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "protocol_decision")
}

func TestSmartProtocolTurnTimingBoundAndOwnership(t *testing.T) {
	c := New(time.Now(), 0)
	ctx := With(context.Background(), c)
	event, content := 2, 30
	RecordWSTurn(ctx, WSTurnTiming{Turn: 1, Upstream: "ws", FirstUpstreamEventMS: &event, FirstActualContentMS: &content})
	event = 99
	content = 99
	for i := 2; i <= 130; i++ {
		RecordWSTurn(ctx, WSTurnTiming{Turn: i, Upstream: "http"})
	}
	var got Snapshot
	c.WhenFinished(func(s Snapshot) { got = s })
	c.Finish(200, false)
	require.Len(t, got.WSTurns, 128)
	require.True(t, got.Truncated)
	require.Equal(t, 2, *got.WSTurns[0].FirstUpstreamEventMS)
	require.Equal(t, 30, *got.WSTurns[0].FirstActualContentMS)
}
