package openai_ws_v2

import (
	"context"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"sync/atomic"
	"testing"
	"time"
)

type semanticClockConn struct {
	FrameConn
	tick *atomic.Int64
}

func (c *semanticClockConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	k, p, e := c.FrameConn.ReadFrame(ctx)
	if e == nil {
		c.tick.Add(100)
	}
	return k, p, e
}
func TestRelaySemanticFirstToken(t *testing.T) {
	for _, tc := range []struct {
		name, event string
		want        *int
	}{
		{"reasoning", `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`, semanticInt(200)},
		{"empty_delta", `{"type":"response.output_text.delta","delta":""}`, nil},
		{"text", `{"type":"response.output_text.delta","delta":"hello"}`, semanticInt(200)},
		{"tool", `{"type":"response.function_call_arguments.delta","delta":"{}"}`, semanticInt(200)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tick atomic.Int64
			base := time.Now()
			frames := []passthroughTestFrame{}
			for _, e := range []string{`{"type":"response.created","response":{"id":"resp_synthetic"}}`, tc.event, `{"type":"response.metadata"}`, `{"type":"response.completed","response":{"id":"resp_synthetic"}}`} {
				frames = append(frames, passthroughTestFrame{msgType: coderws.MessageText, payload: []byte(e)})
			}
			upstream := &semanticClockConn{FrameConn: newPassthroughTestFrameConn(frames, true), tick: &tick}
			var turn RelayTurnResult
			result, exit := Relay(context.Background(), newPassthroughTestFrameConn(nil, false), upstream, []byte(`{"type":"response.create","model":"synthetic"}`), RelayOptions{Now: func() time.Time { return base.Add(time.Duration(tick.Load()) * time.Millisecond) }, FirstTurnStartedAt: base, OnTurnComplete: func(r RelayTurnResult) { turn = r }})
			require.Nil(t, exit)
			require.Equal(t, base, turn.StartedAt, "preserve accepted-frame origin")
			require.Equal(t, tc.want, turn.FirstTokenMs, "first semantic observation, never terminal/empty delta")
			require.Equal(t, tc.want, result.FirstTokenMs)
			require.Equal(t, 400*time.Millisecond, turn.Duration)
		})
	}
}
func semanticInt(v int) *int { return &v }
