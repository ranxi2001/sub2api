//go:build unit && inheritedmodel

package service

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type pendingObservationCache struct {
	get       func(context.Context, int) (string, error)
	reads     int
	publishes int
}

func (c *pendingObservationCache) GetOpenAIAgentRecovery(ctx context.Context, _ string) (string, error) {
	c.reads++
	return c.get(ctx, c.reads)
}

func (c *pendingObservationCache) PublishOpenAIAgentRecovery(context.Context, string, string, time.Duration) error {
	c.publishes++
	return nil
}

// Fake time advances only when the synctest bubble is durably blocked. The
// cache either blocks inside Get until THIS wait context ends, or returns an
// independent error while that context is live. No sleeps, timing tolerance,
// Redis scheduling luck, or diagnostic data granting authorization.
func TestOpenAIAgentPendingObservation(t *testing.T) {
	for _, scenario := range []string{
		"pending_get_deadline", "pending_get_cancel", "latest_pending_get_deadline", "pending_timer_deadline",
		"initial_backend_error", "initial_backend_deadline", "pending_backend_error", "pending_backend_deadline",
		"completed", "failed", "conflicted", "missing",
	} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc := &OpenAIGatewayService{}
				producer := openAIAgentRecoveryBinding{accountID: 41, config: [32]byte{1}, wire: [32]byte{2}}
				route := encodeOpenAIAgentRecoveryBinding(producer)
				pending := "pending:fixture-first:" + route
				latest := "pending:fixture-latest:" + route
				waitFor := 20 * time.Millisecond
				if scenario == "pending_timer_deadline" {
					waitFor = time.Millisecond // strictly before the production poll timer
				}
				ctx, cancel := context.WithTimeout(context.Background(), waitFor)
				defer cancel()
				backendErr := errors.New("fixture independent cache failure")
				cache := &pendingObservationCache{}
				cache.get = func(got context.Context, read int) (string, error) {
					require.Same(t, ctx, got, "the cache must receive the exact wait context")
					if scenario == "initial_backend_error" {
						return "", backendErr
					}
					if scenario == "initial_backend_deadline" {
						return "", context.DeadlineExceeded // backend's deadline, not this live context
					}
					if read == 1 {
						return pending, nil
					}
					switch scenario {
					case "latest_pending_get_deadline":
						if read == 2 {
							return latest, nil
						}
						fallthrough
					case "pending_get_deadline":
						<-got.Done()
						return "", got.Err()
					case "pending_get_cancel":
						cancel() // synchronous cancellation during the second Get
						return "", got.Err()
					case "pending_backend_error":
						return "", backendErr
					case "pending_backend_deadline":
						return "", context.DeadlineExceeded
					case "completed":
						return route, nil
					case "failed":
						return "failed", nil
					case "conflicted":
						return "conflict", nil
					case "missing":
						return "", nil
					default:
						t.Fatal("timer branch must end before a second cache read")
						return "", backendErr
					}
				}
				binding, found, err := svc.awaitOpenAIAgentRecovery(ctx, cache, &svc.agentMessageRecovery, [32]byte{7})
				require.Zero(t, cache.publishes, "waiting never writes an ownership grant")
				counts := openAIRecoveryOwnerCounts{observed: true, total: 3, completed: 2, unexamined: 1}
				counts.observe(binding, found, err)
				reason := func(want string) {
					t.Helper()
					var recovery *OpenAIAgentRecoveryError
					require.ErrorAs(t, err, &recovery)
					require.Equal(t, want, recovery.Reason)
					require.False(t, found, "diagnostic evidence never authorizes a completed owner")
				}
				switch scenario {
				case "pending_get_deadline", "pending_get_cancel", "latest_pending_get_deadline", "pending_timer_deadline":
					reason("delivery_pending")
					require.Error(t, ctx.Err())
					reads := 2
					wantPending := pending
					if scenario == "pending_timer_deadline" {
						reads = 1
					}
					if scenario == "latest_pending_get_deadline" {
						reads, wantPending = 3, latest
					}
					require.Equal(t, reads, cache.reads)
					require.Equal(t, 2, counts.completed)
					require.Zero(t, counts.unexamined)
					require.Equal(t, 1, counts.pending, "PENDING_OBSERVATION_LOST: wait context ended after observed pending; refusal must retain owner_pending=1")
					require.Zero(t, counts.unknown)
					require.Equal(t, wantPending, binding.pending)
					require.Equal(t, route, encodeOpenAIAgentRecoveryBinding(binding))
				case "initial_backend_error", "initial_backend_deadline", "pending_backend_error", "pending_backend_deadline":
					reason("store_unavailable")
					require.NoError(t, ctx.Err(), "independent backend errors must not be reclassified as wait expiry")
					require.Empty(t, binding.pending)
					require.Zero(t, counts.pending)
					require.Equal(t, 1, counts.unknown)
					reads := 2
					if scenario == "initial_backend_error" || scenario == "initial_backend_deadline" {
						reads = 1
					}
					require.Equal(t, reads, cache.reads)
				case "completed":
					require.NoError(t, err)
					require.True(t, found)
					require.Empty(t, binding.pending)
					require.Equal(t, route, encodeOpenAIAgentRecoveryBinding(binding))
					require.Equal(t, 3, counts.completed)
				case "failed":
					reason("owner_unavailable")
					require.True(t, binding.failed)
					require.Equal(t, 1, counts.failed)
				case "conflicted":
					require.NoError(t, err)
					require.True(t, found)
					require.True(t, binding.conflicted, "caller retains its existing conflict rejection")
					require.Equal(t, 1, counts.conflicted)
				case "missing":
					reason("owner_unavailable")
					require.Equal(t, 1, counts.unknown)
				}
				t.Log("PENDING_OBSERVATION_CONTROL_OK: classification preserved; no ownership publication")
			})
		})
	}
}
