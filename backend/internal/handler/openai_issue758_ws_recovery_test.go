//go:build unit

package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise the upgraded-socket error framing with a loopback-only connection.
// This does not claim to cover subsequent WS turns or a real OAuth provider.
func TestIssue758WSInitialRecoveryErrorCarriesWaitGuidance(t *testing.T) {
	handler := &OpenAIGatewayHandler{}
	require.False(t, handler.handleOpenAIWSAgentRecoveryError(context.Background(), nil, errors.New("fixture unrelated error")))
	for _, test := range []struct {
		reason, errorType string
		status            int
		closeStatus       coderws.StatusCode
	}{
		{"owner_rate_limited", "rate_limit_error", http.StatusTooManyRequests, coderws.StatusTryAgainLater},
		{"owner_overloaded", "server_error", http.StatusServiceUnavailable, coderws.StatusTryAgainLater},
		{"route_changed", "invalid_request_error", http.StatusConflict, coderws.StatusPolicyViolation},
		{"http_previous_response_unsupported", "invalid_request_error", http.StatusConflict, coderws.StatusPolicyViolation},
	} {
		t.Run(test.reason, func(t *testing.T) {
			recovery := &service.OpenAIAgentRecoveryError{Reason: test.reason}
			if test.closeStatus == coderws.StatusTryAgainLater {
				recovery.RetryAt = time.Now().UTC().Add(5 * time.Hour).Truncate(time.Second)
			}
			type serverResult struct {
				handled bool
				err     error
			}
			done := make(chan serverResult, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					done <- serverResult{err: err}
					return
				}
				defer func() { _ = conn.CloseNow() }()
				done <- serverResult{handled: handler.handleOpenAIWSAgentRecoveryError(r.Context(), conn, fmt.Errorf("fixture wrapped recovery: %w", recovery))}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, response, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = conn.CloseNow() }()
			require.Equal(t, http.StatusSwitchingProtocols, response.StatusCode)
			kind, payload, err := conn.Read(ctx)
			require.NoError(t, err)
			require.Equal(t, coderws.MessageText, kind)
			require.Equal(t, "error", gjson.GetBytes(payload, "type").String())
			require.EqualValues(t, test.status, gjson.GetBytes(payload, "status").Int())
			require.Equal(t, test.errorType, gjson.GetBytes(payload, "error.type").String())
			require.Equal(t, "encrypted_agent_recovery_"+test.reason, gjson.GetBytes(payload, "error.code").String())
			message := gjson.GetBytes(payload, "error.message").String()
			require.Equal(t, recovery.ClientMessage(), message, "the complete action guidance must not be truncated to a WS close reason")
			require.Contains(t, message, "Preserve this task and context")
			if !recovery.RetryAt.IsZero() {
				require.Contains(t, message, recovery.RetryAt.Format(time.RFC3339))
			}
			_, _, err = conn.Read(ctx)
			require.Equal(t, test.closeStatus, coderws.CloseStatus(err))
			select {
			case result := <-done:
				require.NoError(t, result.err)
				require.True(t, result.handled)
			case <-ctx.Done():
				t.Fatal("recovery error handler did not complete after the client received its close frame")
			}
		})
	}
}
