package mihomo

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBPSQualityDecayAndBoundedHistory(t *testing.T) {
	now := time.Now()
	var r bpsQualityRate
	require.Equal(t, 0.75, r.rate(now))
	for i := 0; i < 1000; i++ {
		r.observe(false, now)
	}
	require.LessOrEqual(t, r.samples, 64.0)
	require.Less(t, r.rate(now), 0.1)
	require.Greater(t, r.rate(now.Add(10*bpsQualityHalfLife)), 0.7)
	for i := 0; i < 1000; i++ {
		r.observe(true, now)
	}
	require.Greater(t, r.rate(now), 0.9)
}

func TestBPSQualitySelectionAndAffinity(t *testing.T) {
	m := bpsTestManager(t)
	now := time.Now()
	old, release, err := m.acquireBPSSession("old", now)
	require.NoError(t, err)
	release()
	var bad, good string
	for id, port := range m.bpsPorts {
		if fmt.Sprintf("http://127.0.0.1:%d", port) == old {
			bad = id
		} else {
			good = id
		}
	}
	for i := 0; i < 20; i++ {
		m.bpsHealthLocked(bad).modelQuality.observe(false, now)
		m.bpsHealthLocked(bad).connectQuality.observe(true, now)
		m.bpsHealthLocked(good).modelQuality.observe(true, now)
		m.bpsHealthLocked(good).connectQuality.observe(true, now)
	}
	// Ranking compares reachable exits; an unverified fast race is tested separately.
	for node, port := range m.bpsPorts {
		require.NoError(t, m.checkBPSHealth(context.Background(), node, fmt.Sprintf("http://127.0.0.1:%d", port)))
	}
	lease, err := m.probeBPSLease(context.Background(), "new", nil)
	require.NoError(t, err)
	require.Equal(t, good, lease.node)
	lease.Release()
	pinned, release, err := m.acquireBPSSession("old", now)
	require.NoError(t, err)
	release()
	require.Equal(t, old, pinned, "healthy affinity survives a ranking change")
	m.bpsFailureLocked(bad, now)
	lease, err = m.probeBPSLease(context.Background(), "old", nil)
	require.NoError(t, err)
	require.Equal(t, good, lease.node)
	lease.Release()
	require.Less(t, m.bpsQualityScoreLocked(good, 100, 100, now), m.bpsQualityScoreLocked(bad, 0, 0, now), "avoid overloading one successful node")
	m.bpsHealthLocked(good).retryAfter = now.Add(time.Minute)
	lease, err = m.probeBPSLease(context.Background(), "another", nil)
	require.NoError(t, err)
	require.Equal(t, bad, lease.node, "a high score cannot bypass cooldown")
	lease.Release()
}

func TestBPSQualityFeedbackDeduplicatedAndCacheHitsExcluded(t *testing.T) {
	m := bpsTestManager(t)
	lease, err := m.probeBPSLease(context.Background(), "a", nil)
	require.NoError(t, err)
	h := m.bpsHealth[lease.node]
	require.Equal(t, 1.0, h.connectQuality.samples)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); lease.ReportSuccess() }()
	}
	wg.Wait()
	require.Equal(t, 1.0, h.modelQuality.samples)
	require.Equal(t, 1.0, h.modelQuality.successes)
	lease.Release()
	for i := 0; i < 10; i++ {
		next, err := m.probeBPSLease(context.Background(), "a", nil)
		require.NoError(t, err)
		next.Release()
	}
	require.Equal(t, 1.0, h.connectQuality.samples)
	require.Equal(t, 1.0, h.modelQuality.samples)
	failed, err := m.probeBPSLease(context.Background(), "a", nil)
	require.NoError(t, err)
	failed.ReportStreamFailure()
	failed.ReportSuccess()
	failed.Release()
	require.InDelta(t, 2.0, h.modelQuality.samples, 0.01)
	require.InDelta(t, 1.0, h.modelQuality.successes, 0.01)
}
