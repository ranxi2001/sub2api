package openai_ws_v2

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// 1006 is a typed transport outcome, never a valid close frame on the wire.
// Use FrameConn to exercise it alongside real-wire 1000/1001/1005 coverage.
func TestRelayUpstreamCloseCause(t *testing.T) {
	for _, status := range []coderws.StatusCode{1000, 1001, 1005, 1006} {
		t.Run(fmt.Sprint(int(status)), func(t *testing.T) {
			original := coderws.CloseError{Code: status, Reason: "typed-private-close-canary"}
			upstream := &eofReplacementFrameConn{
				FrameConn: newPassthroughTestFrameConn(nil, true),
				err:       fmt.Errorf("transport read: %w", original),
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, exit := Relay(ctx, newPassthroughTestFrameConn(nil, false), upstream,
				[]byte(`{"type":"response.create","model":"gpt-5.1","input":[]}`), RelayOptions{})
			require.NotNil(t, exit)
			require.Equal(t, "read_upstream", exit.Stage)
			require.False(t, exit.Graceful)
			var got coderws.CloseError
			require.True(t, errors.As(exit.Err, &got), "ISSUE906_TYPED_CLOSE_CAUSE_LOST")
			require.Equal(t, original, got, "routing must retain the complete original typed cause")
		})
	}
}
