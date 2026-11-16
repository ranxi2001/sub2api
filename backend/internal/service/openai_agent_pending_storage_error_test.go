//go:build unit && inheritedmodel

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// End the wait inside the second GET, after pending was observed. This avoids
// racing the polling timer and proves which error the GET actually returned.
func TestOpenAIAgentPendingStorageError(t *testing.T) {
	backendErr := errors.New("fixture independent cache failure")
	for _, tc := range []struct {
		name        string
		end         string
		readErr     error
		own, wrap   bool
		wantPending bool
	}{
		{"own_deadline", "deadline", nil, true, false, true},
		{"own_cancel", "cancel", nil, true, false, true},
		{"wrapped_own_deadline", "deadline", nil, true, true, true},
		{"wrapped_own_cancel", "cancel", nil, true, true, true},
		{"backend_error_at_deadline", "deadline", backendErr, false, false, false},
		{"backend_error_at_cancel", "cancel", backendErr, false, false, false},
		{"backend_deadline_at_cancel", "cancel", context.DeadlineExceeded, false, true, false},
		{"backend_cancel_at_deadline", "deadline", context.Canceled, false, true, false},
		{"backend_error_while_live", "", backendErr, false, false, false},
		{"backend_deadline_while_live", "", context.DeadlineExceeded, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()
				svc := &OpenAIGatewayService{}
				producer := openAIAgentRecoveryBinding{accountID: 41, config: [32]byte{1}, wire: [32]byte{2}}
				route := encodeOpenAIAgentRecoveryBinding(producer)
				pending := "pending:fixture-storage-error:" + route
				var returnedErr error
				cache := &pendingObservationCache{get: func(got context.Context, read int) (string, error) {
					require.Same(t, ctx, got)
					if read == 1 {
						return pending, nil
					}
					require.Equal(t, 2, read, "a storage error must end the wait without replay")
					switch tc.end {
					case "deadline":
						<-got.Done()
					case "cancel":
						cancel()
					}
					returnedErr = tc.readErr
					if tc.own {
						returnedErr = got.Err()
					}
					if tc.wrap {
						returnedErr = fmt.Errorf("fixture cache GET: %w", returnedErr)
					}
					return "", returnedErr
				}}
				binding, found, err := svc.awaitOpenAIAgentRecovery(ctx, cache, &svc.agentMessageRecovery, [32]byte{7})
				require.Equal(t, 2, cache.reads)
				require.Zero(t, cache.publishes, "waiting never publishes an ownership grant")
				require.False(t, found, "diagnostics must never authorize an owner")
				if tc.end == "" {
					require.NoError(t, ctx.Err())
				} else {
					require.Error(t, ctx.Err(), "the wait must end before the GET returns")
				}
				require.Equal(t, tc.own, ctx.Err() != nil && errors.Is(returnedErr, ctx.Err()))
				var recovery *OpenAIAgentRecoveryError
				require.ErrorAs(t, err, &recovery)
				counts := openAIRecoveryOwnerCounts{observed: true, total: 3, completed: 2, unexamined: 1}
				counts.observe(binding, found, err)
				if tc.wantPending {
					require.Equal(t, "delivery_pending", recovery.Reason)
					require.Equal(t, pending, binding.pending)
					require.Equal(t, route, encodeOpenAIAgentRecoveryBinding(binding))
					require.Equal(t, 1, counts.pending)
					require.Zero(t, counts.unknown)
				} else {
					require.Equal(t, "store_unavailable", recovery.Reason, "PENDING_STORAGE_ERROR_MASKED: independent GET failure must remain store_unavailable")
					require.Empty(t, binding.pending)
					require.Zero(t, counts.pending)
					require.Equal(t, 1, counts.unknown)
				}
				require.Equal(t, 2, counts.completed)
				require.Zero(t, counts.unexamined)
				t.Log("PENDING_STORAGE_ERROR_CONTROL_OK: classification preserved; found=false; no ownership publication")
			})
		})
	}
}
