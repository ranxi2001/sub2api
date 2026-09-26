package mihomo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

type bpsCandidate struct {
	node, proxy string
	generation  uint64
	score       float64
	lastProbe   time.Time
	verified    bool
}

// Probe candidates without creating session leases. Only the winning exit is
// bound, so parallel preflight neither consumes session capacity nor pins a
// conversation to a failed probe while its other probes are still running.
func (m *Manager) bpsProbeCandidates(scope string, excluded map[string]bool) ([]bpsCandidate, string, error) {
	digest := sha256.Sum256([]byte(scope))
	key := hex.EncodeToString(digest[:])
	m.bpsMu.Lock()
	defer m.bpsMu.Unlock()
	now := time.Now()
	eligible, err := m.bpsEligibleNodesLocked(now, excluded)
	if err != nil {
		return nil, "", err
	}
	loads, activeLoads := m.bpsSessionLoadsLocked(now)
	binding := m.bpsSessions[key]
	previous := ""
	if binding != nil {
		previous = binding.node
		if binding.active > 0 && (binding.failed || !eligible[binding.node]) {
			return nil, previous, bpsSelectionError("session_draining", "bound BPS node unavailable while requests are active")
		}
	} else if len(m.bpsSessions) >= bpsMaxSessions {
		return nil, previous, bpsSelectionError("session_capacity", "BPS session capacity exceeded")
	}
	candidates := make([]bpsCandidate, 0, len(eligible))
	for node := range eligible {
		proxy := m.bpsStatic[node]
		if !m.bpsStaticMode {
			port, ok := m.bpsPorts[node]
			if !ok {
				continue
			}
			proxy = fmt.Sprintf("http://127.0.0.1:%d", port)
		}
		h := m.bpsHealthAtLocked(node, now)
		candidate := bpsCandidate{lastProbe: h.lastProbe, node: node, proxy: proxy, generation: h.generation, score: m.bpsQualityScoreLocked(node, activeLoads[node], loads[node], now), verified: now.Before(h.verifiedUntil)}
		if binding != nil && binding.node == node && !binding.failed && (binding.active > 0 || candidate.verified) {
			// Preserve a verified affinity and all in-flight requests. An idle,
			// unverified binding may compete with alternative candidates.
			return []bpsCandidate{candidate}, previous, nil
		}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].verified != candidates[j].verified {
			return candidates[i].verified
		}
		if !candidates[i].verified && !candidates[i].lastProbe.Equal(candidates[j].lastProbe) {
			return candidates[i].lastProbe.Before(candidates[j].lastProbe)
		}
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].node < candidates[j].node
	})
	if len(candidates) == 0 {
		return nil, previous, bpsSelectionError("no_eligible_nodes", "no eligible BPS proxy nodes")
	}
	if len(candidates) > bpsMaxCandidateProbes {
		candidates = candidates[:bpsMaxCandidateProbes]
	}
	return candidates, previous, nil
}

func (m *Manager) probeBPSLease(ctx context.Context, scope string, excluded map[string]bool) (_ *BPSLease, resultErr error) {
	checked := 0
	defer func() { resultErr = bpsAcquisitionError(resultErr, checked) }()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	candidates, previous, err := m.bpsProbeCandidates(scope, excluded)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(scope))
	key := hex.EncodeToString(digest[:])

	bind := func(candidate bpsCandidate) (*BPSLease, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !m.bpsNodeStillEligible(candidate.node, candidate.generation) {
			return nil, bpsSelectionError("candidate_changed", "BPS candidate changed during reachability check")
		}
		proxy, release, err := m.acquireBPSPreferredSession(scope, time.Now(), excluded, candidate.node)
		if err != nil {
			return nil, err
		}
		// Another request can bind this same conversation while we are probing.
		// Keep that exit only if it is still verified; never move an active lease.
		m.bpsMu.Lock()
		binding := m.bpsSessions[key]
		node := binding.node
		generation := m.bpsHealthAtLocked(node, time.Now()).generation
		m.bpsMu.Unlock()
		if !m.bpsNodeStillEligible(node, generation) {
			release()
			return nil, bpsSelectionError("session_draining", "bound BPS node is no longer verified")
		}
		return &BPSLease{ProxyURL: proxy, manager: m, node: node, generation: generation, release: release}, nil
	}
	// Reuse a previously verified winner without launching unnecessary probes.
	for _, candidate := range candidates {
		if !candidate.verified {
			break
		}
		if lease, err := bind(candidate); err == nil {
			m.logBPSLeaseSelected(ctx, key, previous, lease)
			return lease, nil
		}
	}
	probeCtx, cancelProbes := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { cancelProbes(); wg.Wait() }()
	type result struct {
		candidate bpsCandidate
		err       error
	}
	results := make(chan result, bpsProbeParallelism)
	started, active := 0, 0
	launch := func() {
		candidate := candidates[started]
		started++
		active++
		checked++
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := m.checkBPSHealth(probeCtx, candidate.node, candidate.proxy)
			select {
			case results <- result{candidate: candidate, err: err}:
			case <-probeCtx.Done():
			}
		}()
	}
	for active < bpsProbeParallelism && started < len(candidates) {
		launch()
	}
	for active > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case completed := <-results:
			active--
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if completed.err == nil {
				if lease, err := bind(completed.candidate); err == nil {
					cancelProbes()
					m.logBPSLeaseSelected(ctx, key, previous, lease)
					return lease, nil
				}
			}
			if started < len(candidates) {
				launch()
			}
		}
	}
	if _, _, err := m.bpsProbeCandidates(scope, excluded); err != nil {
		return nil, err
	}
	return nil, bpsSelectionError("candidate_checks_exhausted", "BPS proxy reachability checks exhausted")
}
