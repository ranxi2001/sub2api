package mihomo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func bpsDynamicTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	m := bpsTestManager(t)
	raw := []string{"http://session-one:local-test@proxy.example:2000"}
	nodes, names, err := dynamicProxyNodes(raw)
	require.NoError(t, err)
	m.saved = saved{DynamicProxies: raw, Nodes: nodes, NodeNames: names}
	_, err = m.config(m.saved)
	require.NoError(t, err)
	return m, harvestDigest(nodes[0])
}

func bpsTestBinding(m *Manager, scope string) *bpsSession {
	digest := sha256.Sum256([]byte(scope))
	return m.bpsSessions[hex.EncodeToString(digest[:])]
}

func TestBPSDynamicSourceUsesConfiguredOutbound(t *testing.T) {
	m, dynamic := bpsDynamicTestManager(t)
	// An HTTP subscription or a DYNAMIC-looking name is not a dynamic input.
	for _, name := range []string{"ordinary-http", "DYNAMIC-subscription-node"} {
		n := map[string]any{"name": name, "type": "http", "server": "proxy.example", "port": 2000}
		m.saved.Nodes = append(m.saved.Nodes, n)
	}
	_, err := m.config(m.saved)
	require.NoError(t, err)
	require.True(t, m.bpsDynamic[dynamic])
	for _, n := range m.saved.Nodes[1:] {
		require.False(t, m.bpsDynamic[harvestDigest(n)])
	}
	// Reloading persisted JSON converts numeric fields; classification survives.
	raw, err := json.Marshal(m.saved)
	require.NoError(t, err)
	var reloaded saved
	require.NoError(t, json.Unmarshal(raw, &reloaded))
	_, err = m.config(reloaded)
	require.NoError(t, err)
	require.True(t, m.bpsDynamic[dynamic])
}

func TestBPSDynamicWindowReassessesOnlyIdleDynamicBindings(t *testing.T) {
	m, dynamic := bpsDynamicTestManager(t)
	now := time.Now()
	_, release, err := m.acquireBPSSession("idle", now)
	require.NoError(t, err)
	release()
	original := bpsTestBinding(m, "idle")
	_, release, err = m.acquireBPSSession("idle", now.Add(bpsDynamicWindow-time.Second))
	require.NoError(t, err)
	require.Same(t, original, bpsTestBinding(m, "idle"))
	release()
	_, activeRelease, err := m.acquireBPSSession("active", now)
	require.NoError(t, err)
	active := bpsTestBinding(m, "active")
	after := now.Add(bpsDynamicWindow)
	_, release, err = m.acquireBPSSession("idle", after)
	require.NoError(t, err)
	require.NotSame(t, original, bpsTestBinding(m, "idle"))
	require.Greater(t, bpsTestBinding(m, "idle").generation, original.generation)
	require.Same(t, active, bpsTestBinding(m, "active"), "never retire an in-flight binding")
	release()
	// Concurrent requests can finish on the existing local endpoint.
	_, concurrentRelease, err := m.acquireBPSSession("active", after)
	require.NoError(t, err)
	require.Same(t, active, bpsTestBinding(m, "active"))
	activeRelease()
	concurrentRelease()
	_, release, err = m.acquireBPSSession("active", after)
	require.NoError(t, err)
	require.NotSame(t, active, bpsTestBinding(m, "active"))
	require.Equal(t, dynamic, bpsTestBinding(m, "active").node, "reassessment need not force a new endpoint")
	release()

	subscription := bpsTestManager(t)
	_, release, err = subscription.acquireBPSSession("sticky", now)
	require.NoError(t, err)
	release()
	sticky := bpsTestBinding(subscription, "sticky")
	_, release, err = subscription.acquireBPSSession("sticky", after)
	require.NoError(t, err)
	require.Same(t, sticky, bpsTestBinding(subscription, "sticky"))
	release()
}

func TestBPSDynamicCooldownAndWindowRequireFreshProbe(t *testing.T) {
	m, node := bpsDynamicTestManager(t)
	now := time.Now()
	for i, cooldown := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 2 * time.Minute} {
		m.bpsStreamFailureLocked(node, now)
		require.Equal(t, now.Add(cooldown), m.bpsHealth[node].retryAfter, "failure %d", i+1)
	}
	h := m.bpsHealth[node]
	h.modelQuality.observe(false, now)
	h.connectQuality.observe(false, now)
	proxy := fmt.Sprintf("http://127.0.0.1:%d", m.bpsPorts[node])
	calls := 0
	m.bpsProbe = func(context.Context, string) error { calls++; return nil }
	require.Error(t, m.checkBPSHealth(context.Background(), node, proxy))
	require.Zero(t, calls)
	// Simulate the local window expiring, not a supplier-confirmed IP change.
	h.windowStarted = now.Add(-bpsDynamicWindow)
	m.bpsHealthAtLocked(node, time.Now())
	require.Zero(t, h.modelQuality.samples)
	require.Zero(t, h.connectQuality.samples)
	require.True(t, h.verifiedUntil.IsZero())
	require.NoError(t, m.checkBPSHealth(context.Background(), node, proxy))
	require.Equal(t, 1, calls, "expiry alone is not verified connectivity")
	require.Equal(t, 1.0, h.connectQuality.samples)
	m.bpsStreamFailureLocked(node, time.Now())
	require.Equal(t, 1, h.streamFailures)
	require.WithinDuration(t, time.Now().Add(30*time.Second), h.retryAfter, time.Second)
}

func TestBPSDynamicLateFeedbackDoesNotAffectNewWindow(t *testing.T) {
	for name, report := range map[string]func(*BPSLease){
		"completed":    (*BPSLease).ReportSuccess,
		"upstream-5xx": (*BPSLease).ReportUpstreamFailure,
		"transport":    (*BPSLease).ReportFailure,
		"stream":       (*BPSLease).ReportStreamFailure,
	} {
		t.Run(name, func(t *testing.T) {
			m, node := bpsDynamicTestManager(t)
			old, err := m.probeBPSLease(context.Background(), "same-session", nil)
			require.NoError(t, err)
			h := m.bpsHealth[node]
			h.windowStarted = time.Now().Add(-bpsDynamicWindow)
			fresh, err := m.probeBPSLease(context.Background(), "same-session", nil)
			require.NoError(t, err)
			require.Greater(t, fresh.generation, old.generation)
			report(old)
			old.Release()
			require.Zero(t, h.modelQuality.samples)
			require.Zero(t, h.failures)
			require.True(t, h.retryAfter.IsZero())
			require.False(t, bpsTestBinding(m, "same-session").failed)
			fresh.ReportSuccess()
			fresh.Release()
			require.Equal(t, 1.0, h.modelQuality.samples)
			require.Equal(t, 1.0, h.modelQuality.successes)
		})
	}
}

func TestBPSDynamicProbeCannotValidateNextWindow(t *testing.T) {
	m, node := bpsDynamicTestManager(t)
	proxy := fmt.Sprintf("http://127.0.0.1:%d", m.bpsPorts[node])
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m.bpsProbe = func(context.Context, string) error {
		if calls.Add(1) == 1 {
			close(started)
			<-finish
		}
		return nil
	}
	result := make(chan error, 1)
	go func() { result <- m.checkBPSHealth(context.Background(), node, proxy) }()
	<-started
	m.bpsMu.Lock()
	h := m.bpsHealth[node]
	pending := h.probing
	h.windowStarted = time.Now().Add(-bpsDynamicWindow)
	m.bpsHealthAtLocked(node, time.Now())
	require.Equal(t, pending, h.probing, "retain probe ownership across reset")
	m.bpsMu.Unlock()
	close(finish)
	require.Error(t, <-result)
	select {
	case <-pending:
	default:
		t.Fatal("stale probe must release its waiters")
	}
	require.True(t, h.verifiedUntil.IsZero())
	require.Zero(t, h.connectQuality.samples)
	require.NoError(t, m.checkBPSHealth(context.Background(), node, proxy))
	require.Equal(t, int32(2), calls.Load())
	require.Equal(t, 1.0, h.connectQuality.samples)
}

func TestBPSDynamicWindowDoesNotBypassAdmission(t *testing.T) {
	for _, policy := range []string{"country", "disabled", "excluded", "probe-fails"} {
		t.Run(policy, func(t *testing.T) {
			m, node := bpsDynamicTestManager(t)
			lease, err := m.probeBPSLease(context.Background(), "session", nil)
			require.NoError(t, err)
			lease.Release()
			m.bpsHealth[node].windowStarted = time.Now().Add(-bpsDynamicWindow)
			var excluded map[string]bool
			switch policy {
			case "country":
				m.saved.CountryFilter = CountryFilter{Mode: "include", Codes: []string{"US"}}
			case "disabled":
				name, _ := m.saved.Nodes[0]["name"].(string)
				m.saved.Disabled = map[string]string{name: "disabled"}
			case "excluded":
				excluded = map[string]bool{node: true}
			case "probe-fails":
				m.bpsProbe = func(context.Context, string) error { return errors.New("mock CONNECT rejected") }
			}
			_, err = m.probeBPSLease(context.Background(), "session", excluded)
			require.Error(t, err)
			for _, binding := range m.bpsSessions {
				require.Zero(t, binding.active)
			}
		})
	}
}
