package mihomo

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBPSHealthQuarantineAndRecovery(t *testing.T) {
	m := bpsTestManager(t)
	first, err := m.probeBPSLease(context.Background(), "a", nil)
	require.NoError(t, err)
	first.ReportFailure()
	first.ReportFailure() // duplicate reporting of an attempt is harmless
	first.Release()
	require.Equal(t, 1, m.bpsHealth[first.node].failures)
	next, err := m.probeBPSLease(context.Background(), "a", nil)
	require.NoError(t, err)
	require.NotEqual(t, first.ProxyURL, next.ProxyURL)
	next.Release()
	// A second independent failure quarantines the old node globally.
	m.bpsMu.Lock()
	m.bpsFailureLocked(first.node, time.Now())
	m.bpsMu.Unlock()
	require.True(t, m.bpsCoolingLocked(first.node, time.Now()))
	require.Error(t, m.checkBPSHealth(context.Background(), first.node, first.ProxyURL))
	calls := 0
	m.bpsProbe = func(context.Context, string) error { calls++; return nil }
	m.bpsHealth[first.node].retryAfter = time.Now().Add(-time.Second)
	require.NoError(t, m.checkBPSHealth(context.Background(), first.node, first.ProxyURL))
	require.Equal(t, 2, calls, "cooldown expiry alone cannot restore the exit")
	require.Zero(t, m.bpsHealth[first.node].failures)
	require.NoError(t, m.checkBPSHealth(context.Background(), first.node, first.ProxyURL))
	require.Equal(t, 2, calls, "successful reachability is cached")
	// Late failure on the retired node must not invalidate the new binding.
	m.bpsMu.Lock()
	m.bpsFailureLocked(first.node, time.Now())
	m.bpsMu.Unlock()
	again, err := m.probeBPSLease(context.Background(), "a", nil)
	require.NoError(t, err)
	require.Equal(t, next.ProxyURL, again.ProxyURL)
	again.Release()
}

func TestBPSHealthPreflightFailoverAndNoDirectFallback(t *testing.T) {
	m := bpsTestManager(t)
	proxy, release, err := m.acquireBPSSession("a", time.Now())
	require.NoError(t, err)
	release()
	calls := map[string]int{}
	var callsMu sync.Mutex
	failed := make(chan struct{})
	m.bpsProbe = func(_ context.Context, p string) error {
		callsMu.Lock()
		calls[p]++
		n := calls[p]
		callsMu.Unlock()
		if p == proxy {
			if n == 2 {
				close(failed)
			}
			return errors.New("proxy timeout")
		}
		<-failed
		return nil
	}
	lease, err := m.probeBPSLease(context.Background(), "a", nil)
	require.NoError(t, err)
	require.NotEqual(t, proxy, lease.ProxyURL)
	require.Equal(t, 2, calls[proxy], "confirm failure before cooldown")
	require.Equal(t, 1, calls[lease.ProxyURL])
	lease.ReportFailure()
	lease.Release()
	m.bpsProbe = func(context.Context, string) error { return errors.New("down") }
	_, err = m.probeBPSLease(context.Background(), "a", nil)
	require.Error(t, err)
	for _, binding := range m.bpsSessions {
		require.Zero(t, binding.active)
	}
}

func TestBPSHealthActiveRequestsDrainBeforeRebind(t *testing.T) {
	m := bpsTestManager(t)
	a, err := m.probeBPSLease(context.Background(), "shared", nil)
	require.NoError(t, err)
	b, err := m.probeBPSLease(context.Background(), "shared", nil)
	require.NoError(t, err)
	a.ReportFailure()
	a.Release()
	_, err = m.probeBPSLease(context.Background(), "shared", nil)
	require.ErrorContains(t, err, "requests are active")
	b.Release()
	replacement, err := m.probeBPSLease(context.Background(), "shared", nil)
	require.NoError(t, err)
	require.NotEqual(t, a.ProxyURL, replacement.ProxyURL)
	// A late report is tied to b's old node, not to the current session.
	b.ReportFailure()
	replacement.Release()
	again, err := m.probeBPSLease(context.Background(), "shared", nil)
	require.NoError(t, err)
	require.Equal(t, replacement.ProxyURL, again.ProxyURL)
	again.Release()
}

func TestBPSHealthSingleFlight(t *testing.T) {
	m := bpsTestManager(t)
	// Existing active affinity probes only its bound exit, once for all waiters.
	_, release, err := m.acquireBPSSession("shared", time.Now())
	require.NoError(t, err)
	defer release()
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m.bpsProbe = func(context.Context, string) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-finish
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := m.probeBPSLease(context.Background(), "shared", nil)
			if err != nil {
				t.Error(err)
				return
			}
			lease.Release()
		}()
	}
	<-started
	close(finish)
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
}

func TestBPSHealthStaleProbeCannotClearNewFailure(t *testing.T) {
	m := bpsTestManager(t)
	first, err := m.probeBPSLease(context.Background(), "a", nil)
	require.NoError(t, err)
	first.Release()
	m.bpsHealth[first.node].verifiedUntil = time.Time{}
	started, finish := make(chan struct{}), make(chan struct{})
	m.bpsProbe = func(context.Context, string) error { close(started); <-finish; return nil }
	done := make(chan error, 1)
	go func() { done <- m.checkBPSHealth(context.Background(), first.node, first.ProxyURL) }()
	<-started
	first.ReportFailure()
	close(finish)
	require.ErrorContains(t, <-done, "failed during")
	require.Equal(t, 1, m.bpsHealth[first.node].failures)
	require.True(t, m.bpsHealth[first.node].verifiedUntil.IsZero())
}

func TestBPSHealthCanceledProbeDoesNotQuarantine(t *testing.T) {
	m := bpsTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	m.bpsProbe = func(context.Context, string) error { cancel(); return context.Canceled }
	_, err := m.probeBPSLease(ctx, "a", nil)
	require.ErrorIs(t, err, context.Canceled)
	for _, h := range m.bpsHealth {
		require.Zero(t, h.failures)
		require.Nil(t, h.probing)
	}
	for _, b := range m.bpsSessions {
		require.Zero(t, b.active)
	}
}

func TestBPSHealthCountryFilterStillFailsClosed(t *testing.T) {
	m := bpsTestManager(t)
	lease, err := m.probeBPSLease(context.Background(), "a", nil)
	require.NoError(t, err)
	lease.Release()
	m.saved.CountryFilter = CountryFilter{Mode: "include", Codes: []string{"US"}}
	_, err = m.probeBPSLease(context.Background(), "a", nil)
	require.ErrorContains(t, err, "no eligible")
}

func TestBPSHealthProbeUsesExplicitProxyWithoutCredentials(t *testing.T) {
	client, err := newBPSProbeClient("http://127.0.0.1:19000")
	require.NoError(t, err)
	defer client.CloseIdleConnections()
	observed := make(chan *http.Request, 1)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		local, remote := net.Pipe()
		go func() {
			defer func() { _ = remote.Close() }()
			req, e := http.ReadRequest(bufio.NewReader(remote))
			if e != nil {
				observed <- nil
				return
			}
			observed <- req
			_, _ = io.WriteString(remote, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		}()
		return local, nil
	}
	require.Error(t, probeBPSHTTPSClient(context.Background(), client))
	req := <-observed
	require.NotNil(t, req)
	require.Equal(t, http.MethodConnect, req.Method)
	require.Equal(t, "bps.openai.com:443", req.Host)
	require.Empty(t, req.Header.Get("Authorization"))
	require.Empty(t, req.Header.Get("Proxy-Authorization"))
	require.Empty(t, req.Header.Get("Chatgpt-Account-Id"))
}

func TestBPSHealthBoundsConsecutiveBadExits(t *testing.T) {
	m := bpsTestManager(t)
	for i := 0; i < bpsMaxCandidateProbes+5; i++ {
		m.saved.Nodes = append(m.saved.Nodes, map[string]any{"name": fmt.Sprintf("extra-%d", i)})
	}
	_, err := m.config(m.saved)
	require.NoError(t, err)
	calls := map[string]int{}
	var callsMu sync.Mutex
	m.bpsProbe = func(_ context.Context, p string) error {
		callsMu.Lock()
		calls[p]++
		callsMu.Unlock()
		return errors.New("unreachable")
	}
	_, err = m.probeBPSLease(context.Background(), "a", nil)
	require.ErrorContains(t, err, "exhausted")
	require.Len(t, calls, bpsMaxCandidateProbes)
	for _, n := range calls {
		require.Equal(t, 2, n)
	}
	for _, b := range m.bpsSessions {
		require.Zero(t, b.active)
	}
}

func TestBPSHealthPoolChangesDuringProbe(t *testing.T) {
	m := bpsTestManager(t)
	m.bpsProbe = func(context.Context, string) error {
		m.mu.Lock()
		m.saved.CountryFilter = CountryFilter{Mode: "include", Codes: []string{"US"}}
		m.mu.Unlock()
		return nil
	}
	_, err := m.probeBPSLease(context.Background(), "a", nil)
	require.ErrorContains(t, err, "no eligible")
}

func TestBPSHealthFailedRecoveryStaysQuarantined(t *testing.T) {
	m := bpsTestManager(t)
	lease, err := m.probeBPSLease(context.Background(), "a", nil)
	require.NoError(t, err)
	lease.ReportFailure()
	lease.Release()
	h := m.bpsHealth[lease.node]
	h.retryAfter = time.Now().Add(-time.Second)
	calls := 0
	m.bpsProbe = func(context.Context, string) error {
		calls++
		if calls == 1 {
			return nil
		}
		return errors.New("failed second recovery check")
	}
	require.Error(t, m.checkBPSHealth(context.Background(), lease.node, lease.ProxyURL))
	require.Equal(t, 2, calls)
	require.True(t, time.Now().Before(h.retryAfter))
	require.True(t, h.verifiedUntil.IsZero())
}

func TestBPSBrokenStreamsDoNotBounceBetweenNodes(t *testing.T) {
	m := bpsTestManager(t)
	first, err := m.probeBPSLease(context.Background(), "conversation", nil)
	require.NoError(t, err)
	first.ReportStreamFailure()
	first.ReportStreamFailure()
	first.Release()
	h := m.bpsHealth[first.node]
	require.Equal(t, 1, h.streamFailures)
	require.WithinDuration(t, time.Now().Add(bpsStreamCooldown), h.retryAfter, time.Second)
	second, err := m.probeBPSLease(context.Background(), "conversation", nil)
	require.NoError(t, err)
	require.NotEqual(t, first.node, second.node)
	second.ReportStreamFailure()
	second.Release()
	_, err = m.probeBPSLease(context.Background(), "conversation", nil)
	require.Error(t, err, "both broken exits must remain quarantined, not bounce back")
	require.Error(t, m.checkBPSHealth(context.Background(), first.node, first.ProxyURL))
	// Recovered HTTPS reachability must not erase recent stream failures.
	h.retryAfter = time.Now().Add(-time.Second)
	require.NoError(t, m.checkBPSHealth(context.Background(), first.node, first.ProxyURL))
	require.Equal(t, 1, h.streamFailures)
	m.bpsStreamFailureLocked(first.node, time.Now())
	require.WithinDuration(t, time.Now().Add(2*bpsStreamCooldown), h.retryAfter, time.Second)
	for i := 0; i < 20; i++ {
		m.bpsStreamFailureLocked(first.node, time.Now())
	}
	require.WithinDuration(t, time.Now().Add(bpsStreamMaxCooldown), h.retryAfter, time.Second)
	// A probe failure cannot shorten a stream cooldown.
	until := h.retryAfter
	m.bpsFailureLocked(first.node, time.Now())
	require.Equal(t, until, h.retryAfter)
	// The penalty eventually decays without requiring real network traffic.
	future := h.lastStreamFailure.Add(bpsStreamFailureWindow + time.Second)
	m.bpsStreamFailureLocked(first.node, future)
	require.Equal(t, 1, h.streamFailures)
	require.Equal(t, future.Add(bpsStreamCooldown), h.retryAfter)
}

func TestBPSTransientLeasesReleaseSessionCapacity(t *testing.T) {
	m := bpsTestManager(t)
	sticky, err := m.probeBPSLease(context.Background(), "sticky", nil)
	require.NoError(t, err)
	sticky.Release()
	for i := 0; i < bpsMaxSessions+10; i++ {
		lease, err := m.acquireScopedBPSLease(context.Background(), fmt.Sprintf("transient:%d", i), true, nil)
		require.NoError(t, err)
		require.Len(t, m.bpsSessions, 2)
		lease.Release()
		lease.Release()
		require.Len(t, m.bpsSessions, 1, "one-shot requests must not wait 30 minutes for eviction")
	}
	// Failed acquisition must release the temporary binding too.
	m.bpsProbe = func(context.Context, string) error { return errors.New("offline") }
	for _, h := range m.bpsHealth {
		h.verifiedUntil = time.Time{}
	}
	_, err = m.acquireScopedBPSLease(context.Background(), "transient:failed", true, nil)
	require.Error(t, err)
	require.Len(t, m.bpsSessions, 1)
}
