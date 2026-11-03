package openai_ws_v2

import (
	"context"
	"errors"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"io"
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

// The upstream emits a fresh sequence only after the relay writes each create.
type semanticTurnsConn struct {
	*semanticClockConn
	sent int
}

func (c *semanticTurnsConn) WriteFrame(ctx context.Context, k coderws.MessageType, p []byte) error {
	c.sent++
	c.tick.Store(int64((c.sent - 1) * 1000))
	base := c.FrameConn.(*passthroughTestFrameConn)
	for _, e := range []string{`{"type":"response.created","response":{"id":"resp_` + string(rune('0'+c.sent)) + `"}}`, `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`, `{"type":"response.output_text.delta","delta":"later"}`, `{"type":"response.completed","response":{"id":"resp_` + string(rune('0'+c.sent)) + `"}}`} {
		base.readCh <- passthroughTestFrame{msgType: k, payload: []byte(e)}
	}
	return nil
}
func TestRelaySemanticFirstTokenTwoTurns(t *testing.T) {
	var tick atomic.Int64
	base := time.Now()
	client := newPassthroughTestFrameConn(nil, false)
	raw := newPassthroughTestFrameConn(make([]passthroughTestFrame, 0, 8), false)
	raw.readCh = make(chan passthroughTestFrame, 8)
	upstream := &semanticTurnsConn{semanticClockConn: &semanticClockConn{FrameConn: raw, tick: &tick}}
	create := []byte(`{"type":"response.create","model":"synthetic"}`)
	var turns []RelayTurnResult
	_, exit := Relay(context.Background(), client, upstream, create, RelayOptions{Now: func() time.Time { return base.Add(time.Duration(tick.Load()) * time.Millisecond) }, FirstTurnStartedAt: base, TakeNextTurnStartedAt: func() time.Time { return base.Add(time.Second) }, OnTurnComplete: func(r RelayTurnResult) {
		turns = append(turns, r)
		if len(turns) == 1 {
			client.readCh <- passthroughTestFrame{msgType: coderws.MessageText, payload: create}
		} else {
			_ = raw.Close()
		}
	}})
	require.Nil(t, exit)
	require.Len(t, turns, 2)
	for i, turn := range turns {
		require.Equal(t, base.Add(time.Duration(i)*time.Second), turn.StartedAt)
		require.Equal(t, semanticInt(200), turn.FirstTokenMs, "later text must not overwrite reasoning; each turn starts independently")
		require.Equal(t, 400*time.Millisecond, turn.Duration)
	}
	require.Equal(t, 2, upstream.sent, "one submission per turn")
}

// Snapshot the real relay result on interrupted streams; no terminal is invented.
func TestRelaySemanticFirstTokenInterrupted(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{{"EOF", io.EOF}, {"cancel", context.Canceled}, {"error", errors.New("synthetic upstream failure")}} {
		for _, reasoning := range []bool{false, true} {
			t.Run(fmtSemanticName(failure.name, reasoning), func(t *testing.T) {
				var tick atomic.Int64
				origin := time.Now()
				frames := []passthroughTestFrame{{msgType: coderws.MessageText, payload: []byte(`{"type":"response.created","response":{"id":"resp_partial"}}`)}}
				if reasoning {
					frames = append(frames, passthroughTestFrame{msgType: coderws.MessageText, payload: []byte(`{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`)})
				}
				upstream := &eofReplacementFrameConn{FrameConn: &semanticClockConn{FrameConn: newPassthroughTestFrameConn(frames, true), tick: &tick}, err: failure.err}
				result, _ := Relay(context.Background(), newPassthroughTestFrameConn(nil, false), upstream, []byte(`{"type":"response.create","model":"synthetic"}`), RelayOptions{Now: func() time.Time { return origin.Add(time.Duration(tick.Load()) * time.Millisecond) }, FirstTurnStartedAt: origin})
				if reasoning {
					require.Equal(t, semanticInt(200), result.FirstTokenMs, "partial reasoning observation must survive reader exit")
				} else {
					require.Nil(t, result.FirstTokenMs, "metadata-only interruption is unknown, never zero")
				}
				require.Nil(t, result.FirstActualContentMs, "empty reasoning structure is semantic activity, not actual content")
				require.EqualValues(t, 1, result.ClientToUpstreamFrames, "no replay on missing content")
			})
		}
	}
}
func fmtSemanticName(name string, reasoning bool) string {
	if reasoning {
		return name + "/reasoning"
	}
	return name + "/metadata"
}
