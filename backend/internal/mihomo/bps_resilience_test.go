package mihomo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestBPSHealthSlowCandidatesLeaveTimeForHealthyExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := bpsTestManager(t)
		for i := 0; i < bpsProbeParallelism; i++ {
			m.saved.Nodes = append(m.saved.Nodes, map[string]any{"name": fmt.Sprintf("extra-%d", i)})
		}
		_, err := m.config(m.saved)
		require.NoError(t, err)
		var mu sync.Mutex
		seen := make(map[string]int)
		m.bpsProbe = func(ctx context.Context, proxy string) error {
			mu.Lock()
			if seen[proxy] == 0 {
				seen[proxy] = len(seen) + 1
			}
			order := seen[proxy]
			mu.Unlock()
			if order > bpsProbeParallelism {
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		}
		start := time.Now()
		lease, err := m.probeBPSLease(context.Background(), "slow-exits", nil)
		require.NoError(t, err, "a slow first wave must leave time for another healthy exit")
		lease.Release()
		require.Greater(t, len(seen), bpsProbeParallelism)
		require.Less(t, time.Since(start), 8*time.Second)
		quarantined := 0
		for _, health := range m.bpsHealth {
			if time.Now().Before(health.retryAfter) {
				quarantined++
			}
		}
		require.Positive(t, quarantined, "completed timeouts must not immediately attract another session")
	})
}

func TestBPSHealthAllSlowCandidatesAreBoundedAndDiagnosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := bpsTestManager(t)
		for i := 0; i < bpsMaxCandidateProbes+2; i++ {
			m.saved.Nodes = append(m.saved.Nodes, map[string]any{"name": fmt.Sprintf("extra-%d", i)})
		}
		_, err := m.config(m.saved)
		require.NoError(t, err)
		core, logs := observer.New(zap.WarnLevel)
		ctx := logger.IntoContext(context.Background(), zap.New(core))
		m.bpsProbe = func(ctx context.Context, _ string) error {
			<-ctx.Done()
			return ctx.Err()
		}
		start := time.Now()
		_, err = m.probeBPSLease(ctx, "all-slow", nil)
		var diagnostic *BPSAcquireError
		require.ErrorAs(t, err, &diagnostic)
		require.Equal(t, "acquisition_timeout", diagnostic.Reason)
		require.Greater(t, diagnostic.Candidates, bpsProbeParallelism)
		require.LessOrEqual(t, diagnostic.Candidates, bpsMaxCandidateProbes)
		require.Equal(t, 15*time.Second, time.Since(start))
		require.Positive(t, logs.FilterMessage("excel_bps.proxy_probe_failed").Len())
		for _, binding := range m.bpsSessions {
			require.Zero(t, binding.active)
		}
	})
}

func TestBPSHealthRecoveryMustFitCandidateBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := bpsTestManager(t)
		lease, err := m.probeBPSLease(context.Background(), "recovery", nil)
		require.NoError(t, err)
		lease.ReportFailure()
		lease.Release()
		h := m.bpsHealth[lease.node]
		h.retryAfter = time.Now().Add(-time.Second)
		calls := 0
		m.bpsProbe = func(ctx context.Context, _ string) error {
			calls++
			if calls == 1 {
				time.Sleep(3 * time.Second)
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		}
		require.Error(t, m.checkBPSHealth(context.Background(), lease.node, lease.ProxyURL))
		require.Equal(t, 2, calls)
		require.True(t, h.verifiedUntil.IsZero(), "one success cannot validate a recovering exit")
		require.True(t, time.Now().Before(h.retryAfter))
	})
}

func TestBPSHealthTransportFailureQuarantinesForOtherSessions(t *testing.T) {
	m := bpsTestManager(t)
	lease, err := m.probeBPSLease(context.Background(), "failed-request", nil)
	require.NoError(t, err)
	lease.ReportFailure()
	lease.Release()
	require.True(t, m.bpsCoolingLocked(lease.node, time.Now()))
	other, err := m.probeBPSLease(context.Background(), "unrelated-request", nil)
	require.NoError(t, err)
	require.NotEqual(t, lease.node, other.node)
	other.Release()
}

func TestBPSHealthProbeConfirmationsProduceOneWarning(t *testing.T) {
	m := bpsTestManager(t)
	lease, err := m.probeBPSLease(context.Background(), "probe-log", nil)
	require.NoError(t, err)
	lease.Release()
	m.bpsHealth[lease.node].verifiedUntil = time.Time{}
	core, logs := observer.New(zap.WarnLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	calls := 0
	m.bpsProbe = func(context.Context, string) error { calls++; return errors.New("offline") }
	require.Error(t, m.checkBPSHealth(ctx, lease.node, lease.ProxyURL))
	require.Equal(t, 2, calls)
	require.Equal(t, 1, logs.FilterMessage("excel_bps.proxy_probe_failed").Len())
}

func TestBPSHealthCallerDeadlineDoesNotPenalizeNode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := bpsTestManager(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.bpsProbe = func(ctx context.Context, _ string) error {
			<-ctx.Done()
			return ctx.Err()
		}
		_, err := m.probeBPSLease(ctx, "client-deadline", nil)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		for _, health := range m.bpsHealth {
			require.Zero(t, health.failures)
			require.True(t, health.retryAfter.IsZero())
			require.Nil(t, health.probing)
		}
	})
}
