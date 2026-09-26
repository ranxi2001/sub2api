package mihomo

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBPSColdPoolNeverProbesOnUserRequest(t *testing.T) {
	m := bpsTestManager(t)
	m.bpsMu.Lock()
	for _, h := range m.bpsHealth {
		h.verifiedUntil = h.verifiedUntil.AddDate(-1, 0, 0)
	}
	m.bpsMu.Unlock()
	var probes atomic.Int32
	m.bpsProbe = func(context.Context, string) error { probes.Add(1); return nil }
	_, err := m.acquireScopedBPSLease(t.Context(), "account:300/cold", false, nil)
	require.Error(t, err, "an empty warm pool must return immediately, not test unknown exits")
	require.Zero(t, probes.Load(), "user requests cannot perform network qualification")
}

func TestBPSWarmPoolTargetsAndReusesScarceReadyNodes(t *testing.T) {
	m := bpsTestManager(t)
	var probes atomic.Int32
	m.bpsProbe = func(context.Context, string) error { probes.Add(1); return nil }
	m.warmBPSPool(t.Context(), 8)
	before := probes.Load()
	require.Positive(t, before)
	status := m.BPSWarmStatus()
	require.Equal(t, 8, status.Target)
	require.Equal(t, 2, status.Ready)
	leases := make([]*BPSLease, 0, 24)
	for i := 0; i < 24; i++ {
		lease, err := m.acquireScopedBPSLease(t.Context(), fmt.Sprintf("account:300/thread:%d", i), false, nil)
		require.NoError(t, err)
		leases = append(leases, lease)
	}
	require.Equal(t, before, probes.Load(), "requests reuse the warm pool without network probes")
	for _, lease := range leases {
		lease.Release()
	}
	m.warmBPSPool(t.Context(), 1)
	require.Equal(t, 1, m.BPSWarmStatus().Target)
	m.warmBPSPool(t.Context(), 0)
	require.Equal(t, 0, m.BPSWarmStatus().Target)
	require.Equal(t, before, probes.Load(), "lowering the target creates no extra probes")
}

func TestBPSWarmFailureCannotReenterUntilBackgroundRecovery(t *testing.T) {
	m := bpsTestManager(t)
	m.warmBPSPool(t.Context(), 2)
	first, err := m.acquireScopedBPSLease(t.Context(), "shared", false, nil)
	require.NoError(t, err)
	first.ReportStreamFailure()
	first.Release()
	var calls atomic.Int32
	m.bpsProbe = func(context.Context, string) error { calls.Add(1); return nil }
	next, err := m.acquireScopedBPSLease(t.Context(), "shared", false, nil)
	require.NoError(t, err)
	require.NotEqual(t, first.node, next.node)
	next.Release()
	require.Zero(t, calls.Load())
	m.bpsMu.Lock()
	m.bpsHealth[first.node].retryAfter = time.Now().Add(-time.Second)
	m.bpsMu.Unlock()
	// Expired cooldown alone is insufficient; only background qualification admits.
	require.False(t, m.bpsNodeStillEligible(first.node, first.generation))
	m.warmBPSPool(t.Context(), 2)
	require.GreaterOrEqual(t, calls.Load(), int32(2))
	require.Equal(t, 2, m.BPSWarmStatus().Ready)
}

func TestBPSWarmCancelStopsProbes(t *testing.T) {
	m := bpsTestManager(t)
	started := make(chan struct{}, 4)
	var active atomic.Int32
	m.bpsProbe = func(ctx context.Context, _ string) error {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { m.warmBPSPool(ctx, 4); close(done) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("warm worker did not stop")
	}
	require.Zero(t, active.Load())
}
