package repository

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Each settings revision gets a fresh pool. In-flight requests finish against
// their snapshot; removed candidates cannot re-populate the new pool.
type astraRoutingUpstream struct {
	tickets         service.AstraTicketLifecycle
	rotationCache   astraRotationCache
	listNodes       func() ([]mihomo.AstraNode, error)
	pinNode         func(context.Context, mihomo.AstraNode) (string, func(), error)
	prepareCallMu   sync.Mutex
	prepareCall     *astraPreparation
	prepareMu       sync.Mutex
	preparer        func(context.Context, int64) error
	retryAfter      time.Time
	preparing       atomic.Bool
	delegate        service.HTTPUpstream
	cfg             *config.Config
	mu              sync.Mutex
	revision        string
	pool            *codexGatewayPinUpstream
	historyRecorder func(service.AstraGatewayHistoryRecord, bool)
}

func (s *astraRoutingUpstream) SetAstraTicketLifecycle(provider service.AstraTicketLifecycle) {
	s.tickets = provider
}
func (s *astraRoutingUpstream) unifiedTickets(ctx context.Context) bool {
	return s.tickets != nil && s.tickets.UnifiedAstraTicketsEnabled(ctx)
}

func (s *astraRoutingUpstream) SetAstraGatewayHistoryRecorder(fn func(service.AstraGatewayHistoryRecord, bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyRecorder = fn
}

func (s *astraRoutingUpstream) current(ctx context.Context) *codexGatewayPinUpstream {
	settings := s.cfg.AstraRouting(ctx)
	poolSettings := settings
	poolSettings.AccountScheduling = false // Scheduling does not invalidate verified routes.
	poolSettings.SchedulingMode = ""
	poolSettings.SchedulingGroupIDs = nil
	encoded, _ := json.Marshal(poolSettings)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !settings.CookiePool.Enabled {
		s.pool = nil
		s.revision = ""
		return nil
	}
	if s.pool == nil || s.revision != string(encoded) {
		s.revision = string(encoded)
		s.pool = &codexGatewayPinUpstream{rotationCache: &s.rotationCache, delegate: s.delegate, config: settings.CookiePool, historyRecorder: s.historyRecorder}
	}
	return s.pool
}
func (s *astraRoutingUpstream) Do(r *http.Request, p string, a int64, n int) (*http.Response, error) {
	return s.DoWithTLS(r, p, a, n, nil)
}
func (s *astraRoutingUpstream) DoWithTLS(r *http.Request, p string, a int64, n int, f *tlsfingerprint.Profile) (*http.Response, error) {
	if r != nil {
		leased, err := service.ValidateOpenAIRequestProxyEgress(r.Context(), a, p)
		if err != nil {
			return nil, err
		}
		if leased {
			return s.delegate.DoWithTLS(r, p, a, n, f)
		}
	}
	if r != nil && service.IsBoundAstraTicketRequest(r.Context()) {
		return s.delegate.DoWithTLS(r, p, a, n, f)
	}
	if r != nil && !service.IsOpenAICodexStateProbeRequest(r.Context()) && codexGatewayPinRequest(r) {
		if s.unifiedTickets(r.Context()) {
			settings := s.cfg.AstraRouting(r.Context())
			if slices.Contains(settings.CookiePool.TargetAccountIDs, a) && codexGatewayPinAstra(r) {
				borrowed := r.Clone(r.Context())
				proxy, release, err := s.tickets.ApplyStoredAstraTarget(r.Context(), borrowed.Header, a, "sse")
				if err != nil {
					if release != nil {
						release()
					}
					if r.Context().Err() != nil {
						return nil, r.Context().Err()
					}
					return s.delegate.DoWithTLS(r, p, a, n, f)
				}
				r = astraFreshRequest(borrowed.Clone(service.WithHTTPUpstreamRedirectsDisabled(r.Context())))
				r.Close = true
				resp, err := s.delegate.DoWithTLS(r, proxy, a, n, f)
				if resp != nil {
					resp.Request = r
				}
				return attachAstraLease(resp, err, release)
			}
			return s.delegate.DoWithTLS(r, p, a, n, f)
		}
		if pool := s.current(r.Context()); pool != nil {
			if slices.Contains(pool.config.TargetAccountIDs, a) && codexGatewayPinAstra(r) {
				cookie, _, effectiveProxy, release, err := s.targetRoute(r, p, a, n, f)
				if err != nil {
					return nil, err
				}
				if cookie == nil {
					release()
					return s.delegate.DoWithTLS(r, p, a, n, f)
				}
				r = r.Clone(service.WithHTTPUpstreamRedirectsDisabled(r.Context()))
				replaceCodexGatewayCookie(r, cookie)
				if pool.config.RotateNodes {
					r = astraFreshRequest(r)
				}
				resp, err := s.delegate.DoWithTLS(r, effectiveProxy, a, n, f)
				return attachAstraLease(resp, err, release)
			}
			return pool.DoWithTLS(r, p, a, n, f)
		}
	}
	return s.delegate.DoWithTLS(r, p, a, n, f)
}
func (s *astraRoutingUpstream) CodexGatewayPinWSHeader(a int64) (string, error) {
	if pool := s.current(context.Background()); pool != nil {
		value, err := pool.CodexGatewayPinWSHeader(a)
		if err == nil {
			return value, nil
		}
	}
	return "", nil
}

func (s *astraRoutingUpstream) SetAstraGatewayPreparer(fn func(context.Context, int64) error) {
	s.prepareMu.Lock()
	s.preparer = fn
	s.prepareMu.Unlock()
}

// Concurrent requests share one preparation, including its failure, instead of
// launching another source scan or failing merely because a scan is running.
type astraPreparation struct {
	done chan struct{}
	pool *codexGatewayPinUpstream
	err  error
}

func (s *astraRoutingUpstream) PrepareAstraGateway(ctx context.Context) error {
	if s.unifiedTickets(ctx) {
		return s.tickets.PrepareStoredAstraSources(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	pool := s.current(ctx)
	s.prepareCallMu.Lock()
	if call := s.prepareCall; call != nil {
		s.prepareCallMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-call.done:
			if err := ctx.Err(); err != nil {
				return err
			}
			if call.pool != pool || s.current(ctx) != pool {
				return errors.New("configuration_changed")
			}
			return call.err
		}
	}
	call := &astraPreparation{done: make(chan struct{}), pool: pool}
	s.prepareCall = call
	s.prepareCallMu.Unlock()
	defer func() {
		s.prepareCallMu.Lock()
		s.prepareCall = nil
		close(call.done)
		s.prepareCallMu.Unlock()
	}()
	call.err = s.prepareAstraGateway(ctx)
	return call.err
}

func (s *astraRoutingUpstream) prepareAstraGateway(ctx context.Context) error {
	s.prepareMu.Lock()
	defer s.prepareMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	pool := s.current(ctx)
	if pool == nil {
		return errors.New("cookie_pool_disabled")
	}
	if pool.config.RotateNodes {
		return s.prepareRotatedSource(ctx, pool)
	}
	if cookie, _ := pool.currentCookie("/backend-api/codex/responses", time.Now()); cookie != nil {
		pool.mu.Lock()
		expires := pool.routes[pool.activeSource].expires
		pool.mu.Unlock()
		// Keep a primary route while its remaining window is healthy; once
		// inside the prewarm window, continue probing for a backup route.
		if time.Until(expires) > 90*time.Second {
			return nil
		}
	}
	if s.preparer == nil {
		return errors.New("source_preparer_unavailable")
	}
	if time.Now().Before(s.retryAfter) {
		return errors.New("source_probe_cooldown")
	}
	s.preparing.Store(true)
	defer s.preparing.Store(false)
	for _, id := range pool.config.SourceAccountIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.current(ctx) != pool {
			return errors.New("configuration_changed")
		}
		probeStart := time.Now()
		if err := s.preparer(ctx, id); err != nil {
			reason := "source_test_failed"
			if err.Error() == "answer_mismatch" {
				reason = "candy_answer_mismatch"
			}
			pool.recordSourceReason(id, probeStart, nil, false, reason)
		}
		if s.current(ctx) != pool {
			return errors.New("configuration_changed")
		}
		if cookie, _ := pool.currentCookie("/backend-api/codex/responses", time.Now()); cookie != nil {
			s.retryAfter = time.Time{}
			return nil
		}
	}
	s.retryAfter = time.Now().Add(15 * time.Second)
	return errors.New("no_qualified_source_route")
}
func (s *astraRoutingUpstream) AstraGatewaySnapshot(ctx context.Context) service.AstraGatewayRuntime {
	if s.unifiedTickets(ctx) {
		return s.tickets.StoredAstraSnapshot(ctx)
	}
	settings := s.cfg.AstraRouting(ctx)
	result := service.AstraGatewayRuntime{GeneratedAt: time.Now(), Revision: settings.Revision, Sources: []service.AstraRouteStatus{}, Targets: []service.AstraRouteStatus{}, WS: []service.AstraWSStatus{}, Preparing: s.preparing.Load()}
	pool := s.current(ctx)
	if pool == nil {
		for _, id := range settings.CookiePool.SourceAccountIDs {
			result.Sources = append(result.Sources, service.AstraRouteStatus{AccountID: id, State: "disabled", Reason: "disabled"})
		}
		return result
	}
	now := time.Now()
	cookie, sourceID := pool.currentCookie("/backend-api/codex/responses", now)
	// Never hold a lock for the duration of a network validation. Snapshot a
	// coherent route/check pair; only an exact current Cookie can be ready.
	pool.targetMu.Lock()
	pool.mu.Lock()
	defer pool.targetMu.Unlock()
	defer pool.mu.Unlock()
	if route, ok := pool.routes[sourceID]; ok && cookie != nil && route.cookie.Value == cookie.Value && now.Before(route.expires) {
		expires := route.expires
		result.RouteExpiresAt = &expires
	}
	result.Cooldowns = s.rotationCache.snapshot(settings.CookiePool.TargetAccountIDs, now)
	result.Gateways = pool.gatewayObservations()
	result.UnknownGatewaySamples = pool.unknownGatewaySamples
	readySource := map[int64]time.Time{}
	failedSource := map[int64]bool{}
	for _, id := range settings.CookiePool.TargetAccountIDs {
		row := service.AstraRouteStatus{AccountID: id, State: "waiting", Reason: "target_not_verified"}
		if check, ok := pool.targetChecks[id]; ok {
			checked := check.checked
			row.CheckedAt = &checked
			row.Gateway = check.gateway
			row.ProxyNode = check.node.Name
			row.ProxyCountry = check.node.Country
			route, exists := pool.routes[check.sourceID]
			current := exists && sha256.Sum256([]byte(route.cookie.Value)) == check.cookieFingerprint && now.Before(route.expires) && now.Before(check.expires) && (!pool.config.RotateNodes || route.node.Identity == check.node.Identity) && (!pool.config.IPAffinity || pool.config.RotateNodes || check.proxyFingerprint == sha256.Sum256([]byte(route.proxy)))
			if current && check.passed {
				expiry := check.expires
				if route.expires.Before(expiry) {
					expiry = route.expires
				}
				row.State = "ready"
				row.Reason = "target_probe_passed"
				row.ExpiresAt = &expiry
				row.RemainingSeconds = max(0, int64(expiry.Sub(now).Seconds()))
				if old, ok := readySource[check.sourceID]; !ok || expiry.Before(old) {
					readySource[check.sourceID] = expiry
				}
			} else if current {
				row.State = "rejected"
				row.Answer = check.answer
				row.Reason = check.reason
				failedSource[check.sourceID] = true
			} else {
				row.State = "waiting"
				row.Reason = "target_not_verified"
			}
		}
		result.Targets = append(result.Targets, row)
	}
	for _, id := range pool.config.SourceAccountIDs {
		row := service.AstraRouteStatus{AccountID: id, State: "waiting", Reason: "not_tested"}
		if prior, ok := pool.statuses[id]; ok {
			row.CheckedAt = prior.CheckedAt
			row.State = prior.State
			row.Reason = prior.Reason
			row.Gateway = prior.Gateway
		}
		if route, ok := pool.routes[id]; ok {
			row.Gateway = astraRoutingHost(route.cookie.Value)
			row.ProxyNode = route.node.Name
			row.ProxyCountry = route.node.Country
			switch {
			case !now.Before(route.expires):
				row.State = "expired"
				row.Reason = "route_expired"
			case !readySource[id].IsZero():
				expiry := readySource[id]
				row.ExpiresAt = &expiry
				row.RemainingSeconds = max(0, int64(expiry.Sub(now).Seconds()))
				row.State = "ready"
				row.Reason = "target_probe_passed"
				row.Active = pool.activeSource == id
				result.ReadyRoutes++
			case failedSource[id]:
				row.State = "candidate"
				row.Reason = "source_passed_target_failed"
			default:
				row.State = "candidate"
				row.Reason = "source_passed_pending_target"
			}
		}
		result.Sources = append(result.Sources, row)
	}
	return result
}

// Only a hostname matching the known gateway namespace is exposed. JWT decoding
// is descriptive metadata, not signature verification or proof of actual routing.
var astraGatewayHostPattern = regexp.MustCompile(`^chat\.gateway\.unified-[0-9]+\.api\.openai\.com$`)

func astraRoutingHost(value string) string {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Host string `json:"host"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return ""
	}
	if !astraGatewayHostPattern.MatchString(claims.Host) {
		return ""
	}
	return claims.Host
}
