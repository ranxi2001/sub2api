package mihomo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
	"sort"
	"time"
)

type bpsWarmContextKey struct{}

// User requests only consume verified exits; qualification is background-only.
func (m *Manager) acquireBPSLease(ctx context.Context, scope string, excluded map[string]bool) (_ *BPSLease, resultErr error) {
	defer func() { resultErr = bpsAcquisitionError(resultErr, 0) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	candidates, previous, err := m.bpsProbeCandidates(scope, excluded)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(scope))
	key := hex.EncodeToString(digest[:])
	for _, candidate := range candidates {
		if !candidate.verified {
			break
		}
		if !m.bpsNodeStillEligible(candidate.node, candidate.generation) {
			continue
		}
		proxy, release, err := m.acquireBPSPreferredSession(scope, time.Now(), excluded, candidate.node)
		if err != nil {
			return nil, err
		}
		m.bpsMu.Lock()
		binding := m.bpsSessions[key]
		node := binding.node
		generation := m.bpsHealthAtLocked(node, time.Now()).generation
		m.bpsMu.Unlock()
		if !m.bpsNodeStillEligible(node, generation) {
			release()
			continue
		}
		lease := &BPSLease{ProxyURL: proxy, manager: m, node: node, generation: generation, release: release}
		m.logBPSLeaseSelected(ctx, key, previous, lease)
		return lease, nil
	}
	return nil, bpsSelectionError("warm_pool_empty", "BPS warm pool has no verified exits; background qualification is pending")
}

// BPSWarmStatus exposes counts only, never supplier credentials or identities.
type BPSWarmStatus struct {
	FailureReasons map[string]int `json:"failure_reasons"`
	Target         int            `json:"target"`
	Ready          int            `json:"ready"`
	Eligible       int            `json:"eligible"`
	Checking       int            `json:"checking"`
	Cooling        int            `json:"cooling"`
}

func (m *Manager) BPSWarmStatus() BPSWarmStatus {
	m.bpsMu.Lock()
	defer m.bpsMu.Unlock()
	now := time.Now()
	s := BPSWarmStatus{Target: m.bpsWarmTarget, FailureReasons: make(map[string]int)}
	eligible, err := m.bpsEligibleNodesLocked(now, nil)
	if err != nil {
		return s
	}
	s.Eligible = len(eligible)
	for node, h := range m.bpsHealth {
		if h.probing != nil {
			s.Checking++
		}
		if now.Before(h.retryAfter) {
			s.Cooling++
			if h.lastFailureReason != "" {
				s.FailureReasons[h.lastFailureReason]++
			}
		}
		if eligible[node] && now.Before(h.verifiedUntil) {
			s.Ready++
		}
	}
	return s
}

// A single service-owned worker refills both sources, never a request goroutine.
func WarmBPSPools(ctx context.Context, managedTarget, staticTarget int) {
	if m := managedManager(); m != nil {
		m.warmBPSPool(ctx, managedTarget)
	}
	bpsStaticManager.warmBPSPool(ctx, staticTarget)
}
func (m *Manager) warmBPSPool(ctx context.Context, target int) {
	m.bpsWarmMu.Lock()
	defer m.bpsWarmMu.Unlock()
	target = max(0, min(target, bpsMaxNodes))
	m.bpsMu.Lock()
	m.bpsWarmTarget = target
	m.bpsMu.Unlock()
	if target == 0 || ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, bpsWarmContextKey{}, true), 8*time.Second)
	defer cancel()
	// Refresh before expiry without withdrawing the still-valid observation.
	m.bpsMu.Lock()
	now := time.Now()
	eligible, _ := m.bpsEligibleNodesLocked(now, nil)
	type refreshCandidate struct {
		bpsCandidate
		active int
	}
	readyNodes := make([]refreshCandidate, 0)
	loads, activeLoads := m.bpsSessionLoadsLocked(now)
	for node := range eligible {
		h := m.bpsHealthAtLocked(node, now)
		if now.Before(h.verifiedUntil) {
			proxy := m.bpsStatic[node]
			if !m.bpsStaticMode {
				proxy = fmt.Sprintf("http://127.0.0.1:%d", m.bpsPorts[node])
			}
			readyNodes = append(readyNodes, refreshCandidate{bpsCandidate: bpsCandidate{node: node, proxy: proxy, lastProbe: h.verifiedUntil, score: m.bpsQualityScoreLocked(node, activeLoads[node], loads[node], now)}, active: activeLoads[node]})
		}
	}
	m.bpsMu.Unlock()
	sort.Slice(readyNodes, func(i, j int) bool { return readyNodes[i].score > readyNodes[j].score })
	due := make([]bpsCandidate, 0)
	for i, c := range readyNodes {
		if (i < target || c.active > 0) && !now.Add(30*time.Second).Before(c.lastProbe) {
			due = append(due, c.bpsCandidate)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].lastProbe.Before(due[j].lastProbe) })
	refreshed := 0
	for _, c := range due {
		if refreshed >= bpsProbeParallelism || ctx.Err() != nil {
			break
		}
		_ = m.checkBPSHealth(ctx, c.node, c.proxy, true)
		refreshed++
	}
	added := 0
	for ctx.Err() == nil {
		m.bpsMu.Lock()
		now = time.Now()
		eligible, err := m.bpsEligibleNodesLocked(now, nil)
		excluded := make(map[string]bool)
		ready := 0
		for node := range eligible {
			if now.Before(m.bpsHealthAtLocked(node, now).verifiedUntil) {
				excluded[node] = true
				ready++
			}
		}
		m.bpsMu.Unlock()
		if err != nil || ready >= target || len(excluded) >= len(eligible) {
			break
		}
		scope := fmt.Sprintf("warm:%d", time.Now().UnixNano())
		lease, err := m.probeBPSLease(ctx, scope, excluded)
		if err != nil {
			break
		}
		lease.Release()
		m.forgetIdleBPSSession(scope)
		added++
	}
	s := m.BPSWarmStatus()
	source := "mihomo"
	if m.bpsStaticMode {
		source = "ip_pool"
	}
	logger.FromContext(ctx).Info("excel_bps.warm_pool_refresh", zap.String("source", source), zap.Int("target", s.Target), zap.Int("ready", s.Ready), zap.Int("eligible", s.Eligible), zap.Int("cooling", s.Cooling), zap.Int("added", added), zap.Int("refreshed", refreshed), zap.Any("failure_reasons", s.FailureReasons))
}
