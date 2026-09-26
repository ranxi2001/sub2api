package mihomo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/transportdiag"
	"go.uber.org/zap"
)

const (
	bpsHealthTTL           = 2 * time.Minute
	bpsFailureCooldown     = time.Minute
	bpsStreamCooldown      = 5 * time.Minute
	bpsStreamMaxCooldown   = 30 * time.Minute
	bpsStreamFailureWindow = 30 * time.Minute
	bpsProbeTimeout        = 5 * time.Second
	// Reserve time for other candidates within the 15-second acquisition budget.
	bpsCandidateTimeout   = 4 * time.Second
	bpsRetryCooldown      = 15 * time.Second
	bpsMaxCandidateProbes = 32
	bpsProbeParallelism   = 4
)

type bpsNodeHealth struct {
	lastFailureReason string
	lastProbe         time.Time
	windowStarted     time.Time
	generation        uint64
	modelQuality      bpsQualityRate
	connectQuality    bpsQualityRate
	verifiedUntil     time.Time
	retryAfter        time.Time
	failures          int
	streamFailures    int
	lastStreamFailure time.Time
	revision          uint64
	probing           chan struct{}
}

// BPSLease keeps the selected exit alive until the response body closes.
// It contains no supplier credentials. Reports always refer to this exit,
// even if a later request has rebound the same session.
type BPSLease struct {
	ProxyURL    string
	manager     *Manager
	node        string
	generation  uint64
	release     func()
	failureOnce sync.Once
}

func (l *BPSLease) Release() { l.release() }

// ReportFailure invalidates cached reachability and the affected bindings.
// Callers must exclude cancellations and application-level HTTP errors.
func (l *BPSLease) ReportFailure() {
	l.failureOnce.Do(func() {
		l.manager.bpsMu.Lock()
		defer l.manager.bpsMu.Unlock()
		now := time.Now()
		h := l.feedbackHealthLocked(now)
		if h == nil {
			return
		}
		h.modelQuality.observe(false, now)
		h.connectQuality.observe(false, now)
		l.manager.bpsFailureLocked(l.node, now)
		if until := now.Add(bpsRetryCooldown); until.After(h.retryAfter) {
			h.retryAfter = until
		}
	})
}

// ReportStreamFailure quarantines even the first broken stream. A short HTTPS
// probe cannot establish that a node can carry a complete model response.
func (l *BPSLease) ReportStreamFailure() {
	l.failureOnce.Do(func() {
		l.manager.bpsMu.Lock()
		defer l.manager.bpsMu.Unlock()
		now := time.Now()
		h := l.feedbackHealthLocked(now)
		if h == nil {
			return
		}
		h.modelQuality.observe(false, now)
		l.manager.bpsStreamFailureLocked(l.node, now)
	})
}

// AcquireBPSLease probes only HTTPS reachability, never model inference.
// Excluded local proxy URLs prevent retrying an already failed request exit.
func AcquireBPSLease(ctx context.Context, scope string, excludedProxyURLs ...string) (*BPSLease, error) {
	return acquireBPSLeaseScoped(ctx, scope, false, excludedProxyURLs...)
}

// AcquireBPSTransientLease uses an isolated request identity and drops its
// binding on failure or final release, so anonymous traffic cannot fill the pool.
func AcquireBPSTransientLease(ctx context.Context, scope string, excludedProxyURLs ...string) (*BPSLease, error) {
	return acquireBPSLeaseScoped(ctx, scope, true, excludedProxyURLs...)
}

func acquireBPSLeaseScoped(ctx context.Context, scope string, transient bool, excludedProxyURLs ...string) (*BPSLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scope == "" {
		return nil, errors.New("BPS session identity required")
	}
	m := managedManager()
	if m == nil {
		return nil, bpsSelectionError("manager_unavailable", "managed Mihomo is not running")
	}
	excluded := make(map[string]bool)
	m.bpsMu.Lock()
	for id, port := range m.bpsPorts {
		for _, proxy := range excludedProxyURLs {
			if proxy == fmt.Sprintf("http://127.0.0.1:%d", port) {
				excluded[id] = true
			}
		}
	}
	m.bpsMu.Unlock()
	return m.acquireScopedBPSLease(ctx, scope, transient, excluded)
}

func (m *Manager) forgetIdleBPSSession(scope string) {
	digest := sha256.Sum256([]byte(scope))
	key := hex.EncodeToString(digest[:])
	m.bpsMu.Lock()
	defer m.bpsMu.Unlock()
	if binding := m.bpsSessions[key]; binding != nil && binding.active == 0 {
		delete(m.bpsSessions, key)
	}
}

func (m *Manager) acquireScopedBPSLease(ctx context.Context, scope string, transient bool, excluded map[string]bool) (*BPSLease, error) {
	if transient {
		defer m.forgetIdleBPSSession(scope)
	}
	lease, err := m.acquireBPSLease(ctx, scope, excluded)
	if err == nil && transient {
		release := lease.release
		lease.release = func() { release(); m.forgetIdleBPSSession(scope) }
	}
	return lease, err
}

// Recheck policy after network I/O: an administrator may change the pool or
// country filter while the probe is running.
func (m *Manager) bpsNodeStillEligible(node string, generation uint64) bool {
	m.bpsMu.Lock()
	defer m.bpsMu.Unlock()
	now := time.Now()
	h := m.bpsHealthAtLocked(node, now)
	if h.generation != generation || !now.Before(h.verifiedUntil) || m.bpsCoolingLocked(node, now) {
		return false
	}
	if m.bpsStaticMode {
		_, ok := m.bpsStatic[node]
		return ok
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !m.state.Running {
		return false
	}
	for _, n := range m.saved.Nodes {
		name, _ := n["name"].(string)
		if harvestDigest(n) == node {
			return bpsEligible(m.saved, name)
		}
	}
	return false
}

func (m *Manager) bpsHealthLocked(node string) *bpsNodeHealth {
	if m.bpsHealth == nil {
		m.bpsHealth = make(map[string]*bpsNodeHealth)
	}
	h := m.bpsHealth[node]
	if h == nil {
		h = &bpsNodeHealth{}
		m.bpsHealth[node] = h
	}
	return h
}

func (m *Manager) bpsCoolingLocked(node string, now time.Time) bool {
	h := m.bpsHealthAtLocked(node, now)
	return now.Before(h.retryAfter)
}

func (m *Manager) bpsFailureLocked(node string, now time.Time) {
	h := m.bpsHealthAtLocked(node, now)
	h.failures++
	h.revision++
	h.verifiedUntil = time.Time{}
	if h.failures >= 2 {
		if until := now.Add(bpsFailureCooldown); until.After(h.retryAfter) {
			h.retryAfter = until
		}
	}
	for _, binding := range m.bpsSessions {
		if binding.node == node {
			binding.failed = true
		}
	}
}

func (m *Manager) bpsStreamFailureLocked(node string, now time.Time) {
	h := m.bpsHealthAtLocked(node, now)
	base, maximum, window := bpsStreamCooldown, bpsStreamMaxCooldown, bpsStreamFailureWindow
	if m.bpsDynamic[node] {
		base, maximum, window = bpsDynamicStreamCooldown, bpsDynamicMaxCooldown, bpsDynamicWindow
	}
	if now.Sub(h.lastStreamFailure) >= window {
		h.streamFailures = 0
	}
	// Bound the streak; repeated concurrent failures must not overflow backoff.
	if h.streamFailures < 4 {
		h.streamFailures++
	}
	h.lastStreamFailure = now
	m.bpsFailureLocked(node, now)
	cooldown := min(base<<(h.streamFailures-1), maximum)
	if until := now.Add(cooldown); until.After(h.retryAfter) {
		h.retryAfter = until
	}
}

// Single-flight by node: hundreds of sessions must not launch hundreds of
// probes. No manager locks are held during I/O. Cancellation is not node failure.
func (m *Manager) checkBPSHealth(ctx context.Context, node, proxy string, force ...bool) error {
	// A candidate's deadline is a proxy failure; the caller's cancellation is
	// not. Keep both contexts so a slow node cannot consume the whole request
	// budget or escape health feedback by exhausting its own probe budget.
	candidateCtx, cancelCandidate := context.WithTimeout(ctx, bpsCandidateTimeout)
	defer cancelCandidate()
	for {
		if err := candidateCtx.Err(); err != nil {
			return err
		}
		m.bpsMu.Lock()
		now := time.Now()
		h := m.bpsHealthAtLocked(node, now)
		if now.Before(h.retryAfter) {
			m.bpsMu.Unlock()
			return errors.New("BPS node is cooling down")
		}
		if now.Before(h.verifiedUntil) && (len(force) == 0 || !force[0]) {
			m.bpsMu.Unlock()
			return nil
		}
		if pending := h.probing; pending != nil {
			m.bpsMu.Unlock()
			select {
			case <-candidateCtx.Done():
				return candidateCtx.Err()
			case <-pending:
				continue
			}
		}
		pending := make(chan struct{})
		h.probing = pending
		h.lastProbe = now
		revision := h.revision
		recovering := h.failures > 0
		m.bpsMu.Unlock()

		probe := m.bpsProbe
		if probe == nil {
			if m.bpsStaticMode {
				probe = probeBPSStaticHTTPS
			} else {
				probe = probeBPSHTTPS
			}
		}
		successes, failures := 0, 0
		probeResults := make([]bool, 0, 3)
		needed := 1
		if recovering {
			needed = 2
		}
		var probeErr error
		// Confirm a first probe failure before cooldown; recovery always requires
		// two consecutive successes. Three network calls is the hard upper bound.
		for n := 0; n < 3; n++ {
			probeCtx, cancel := context.WithTimeout(candidateCtx, bpsProbeTimeout)
			probeErr = probe(probeCtx, proxy)
			if probeErr == nil {
				probeErr = probeCtx.Err()
			}
			cancel()
			if ctx.Err() != nil {
				break
			}
			probeResults = append(probeResults, probeErr == nil)
			if probeErr != nil {
				failures++
				successes = 0
				needed = 2
				if failures >= 2 || recovering || candidateCtx.Err() != nil {
					break
				}
			} else {
				successes++
				if successes >= needed {
					break
				}
			}
		}
		m.bpsMu.Lock()
		m.bpsHealthAtLocked(node, time.Now())
		h.probing = nil
		canceled := ctx.Err() != nil
		stale := revision != h.revision
		if !canceled && !stale {
			for _, success := range probeResults {
				h.connectQuality.observe(success, time.Now())
			}
		}
		if !canceled && !stale {
			if probeErr == nil && successes >= needed {
				h.failures = 0
				h.lastFailureReason = ""
				h.retryAfter = time.Time{}
				h.verifiedUntil = time.Now().Add(bpsHealthTTL)
			} else {
				for n := 0; n < failures; n++ {
					m.bpsFailureLocked(node, time.Now())
				}
				if failures == 0 {
					m.bpsFailureLocked(node, time.Now())
				}
				h.lastFailureReason = bpsProbeFailureReason(probeErr)
				if h.lastFailureReason == "bps_access_denied" || h.lastFailureReason == "proxy_auth_rejected" {
					h.retryAfter = time.Now().Add(bpsStreamCooldown)
				}
				// Even a single budget-exhausting probe gets a short quarantine.
				// Confirmed/repeated failures retain their longer existing cooldown.
				if until := time.Now().Add(bpsRetryCooldown); until.After(h.retryAfter) {
					h.retryAfter = until
				}
			}
		}
		close(pending)
		m.bpsMu.Unlock()
		if canceled {
			return ctx.Err()
		}
		if stale {
			return errors.New("BPS node failed during reachability check")
		}
		if (probeErr != nil || successes < needed) && ctx.Value(bpsWarmContextKey{}) == nil {
			// One summary per failed candidate, not one warning per confirmation.
			logger.FromContext(ctx).Warn("excel_bps.proxy_probe_failed",
				zap.String("node_hash", node[:min(16, len(node))]), zap.String("local_proxy", bpsProxyLogValue(m, proxy)),
				zap.String("error_kind", transportdiag.Classify(probeErr)), zap.String("error_type", fmt.Sprintf("%T", probeErr)),
				zap.Int("probe_attempts", len(probeResults)), zap.Bool("candidate_budget_exhausted", candidateCtx.Err() != nil))
		}
		if probeErr != nil || successes < needed {
			return errors.New("BPS proxy HTTPS reachability failed")
		}
		return nil
	}
}

func probeBPSHTTPS(ctx context.Context, proxy string) error {
	client, err := newBPSProbeClient(proxy)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	return probeBPSHTTPSClient(ctx, client)
}

func newBPSProbeClient(proxy string) (*http.Client, error) {
	parsed, err := url.Parse(proxy)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.User != nil {
		return nil, errors.New("invalid BPS local proxy")
	}
	// Always explicit proxy, verified TLS, fixed non-inference URL, no redirects
	// and no OAuth/session headers. Never inherit a direct/environment fallback.
	transport := &http.Transport{Proxy: http.ProxyURL(parsed), TLSHandshakeTimeout: bpsProbeTimeout, DisableKeepAlives: true}
	client := &http.Client{Transport: transport, Timeout: bpsProbeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, nil
}

func (m *Manager) logBPSLeaseSelected(ctx context.Context, key, previousNode string, lease *BPSLease) {
	node, proxy, generation := lease.node, lease.ProxyURL, lease.generation
	if previousNode != "" && previousNode != node {
		logger.FromContext(ctx).Info("excel_bps.proxy_rebound",
			zap.String("session_hash", key[:16]),
			zap.String("previous_node_hash", previousNode[:min(16, len(previousNode))]),
			zap.String("node_hash", node[:min(16, len(node))]), zap.String("local_proxy", bpsProxyLogValue(m, proxy)))
	}
	if previousNode != node {
		m.bpsMu.Lock()
		health := m.bpsHealthLocked(node)
		now := time.Now()
		modelRate, connectRate := health.modelQuality.rate(now), health.connectQuality.rate(now)
		_, modelSamples := health.modelQuality.decayed(now)
		_, connectSamples := health.connectQuality.decayed(now)
		source := "subscription"
		if m.bpsStaticMode {
			source = "ip_pool"
		} else if m.bpsDynamic[node] {
			source = "dynamic"
		}
		m.bpsMu.Unlock()
		logger.FromContext(ctx).Info("excel_bps.proxy_selected",
			zap.String("proxy_source", source), zap.Uint64("observation_generation", generation),
			zap.String("session_hash", key[:16]), zap.String("node_hash", node[:min(16, len(node))]),
			zap.String("local_proxy", bpsProxyLogValue(m, proxy)), zap.Float64("request_success_rate", modelRate),
			zap.Float64("connectivity_rate", connectRate), zap.Float64("request_samples", modelSamples),
			zap.Float64("connectivity_samples", connectSamples))
	}
}
