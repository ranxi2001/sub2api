package mihomo

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBPSParallelSelectionDoesNotWaitForSlowPreferredNode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := bpsTestManager(t)
		slow, release, err := m.acquireBPSSession("race", time.Now())
		require.NoError(t, err)
		release()
		fast := ""
		for _, port := range m.bpsPorts {
			if proxy := fmt.Sprintf("http://127.0.0.1:%d", port); proxy != slow {
				fast = proxy
			}
		}
		m.bpsProbe = func(ctx context.Context, proxy string) error {
			if proxy == fast {
				time.Sleep(100 * time.Millisecond)
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		}
		start := time.Now()
		lease, err := m.probeBPSLease(context.Background(), "race", nil)
		require.NoError(t, err)
		defer lease.Release()
		require.Equal(t, fast, lease.ProxyURL)
		require.Less(t, time.Since(start), time.Second, "the fast candidate must win without waiting for the preferred proxy timeout")
		for _, h := range m.bpsHealth {
			require.Zero(t, h.failures, "losing a probe race is not evidence of node failure")
		}
	})
}

func TestBPSParallelSelectionRefillsBeforeOtherProbesTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := bpsTestManager(t)
		for i := 0; i < bpsProbeParallelism; i++ {
			m.saved.Nodes = append(m.saved.Nodes, map[string]any{"name": fmt.Sprintf("refill-%d", i)})
		}
		_, err := m.config(m.saved)
		require.NoError(t, err)
		candidates, _, err := m.bpsProbeCandidates("refill", nil)
		require.NoError(t, err)
		bad, fast := candidates[0].proxy, candidates[bpsProbeParallelism].proxy
		firstWave := make(chan struct{})
		var active, maximum, started atomic.Int32
		m.bpsProbe = func(ctx context.Context, proxy string) error {
			n := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
			}
			if started.Add(1) == int32(bpsProbeParallelism) {
				close(firstWave)
			}
			select {
			case <-firstWave:
			case <-ctx.Done():
				return ctx.Err()
			}
			if proxy == bad {
				return errors.New("connection refused")
			}
			if proxy == fast {
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		}
		start := time.Now()
		lease, err := m.probeBPSLease(context.Background(), "refill", nil)
		require.NoError(t, err)
		defer lease.Release()
		require.Equal(t, fast, lease.ProxyURL)
		require.Less(t, time.Since(start), time.Second)
		require.EqualValues(t, bpsProbeParallelism, maximum.Load())
		require.Zero(t, active.Load(), "all losing probes must finish on cancellation")
	})
}
