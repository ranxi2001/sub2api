package repository

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamroute"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

func (s *httpUpstreamService) useRegionalEgress(proxy string, accountID int64, profile service.HTTPUpstreamProfile) bool {
	return (s.upstreamRoutes != nil || s.upstreamRoutesErr != nil) && strings.TrimSpace(proxy) == "" && accountID > 0 &&
		profile != service.HTTPUpstreamProfileOpenAIHarvest && profile != service.HTTPUpstreamProfileExcelBPS
}

// Use one outer Client.Do to retain net/http's redirect credential policy.
// Resolve an egress per RoundTrip so redirected hosts cannot reuse the first
// host's proxy by accident. Each response owns its pool entry until Body.Close.
func (s *httpUpstreamService) doRegionalEgress(req *http.Request, accountID int64, concurrency int, profile service.HTTPUpstreamProfile, fingerprint *tlsfingerprint.Profile) (*http.Response, error) {
	if s.upstreamRoutesErr != nil {
		return nil, s.upstreamRoutesErr
	}
	client := &http.Client{Transport: &regionalUpstreamTransport{s, accountID, concurrency, profile, fingerprint}, CheckRedirect: s.redirectChecker}
	client = s.httpClientForUpstreamRequest(client, req)
	client = httpClientWithGrokAccessDeniedFallback(client)
	// Regional routing adds no replay/failover or transparent direct fallback.
	return doUpstreamRequest(client, req)
}

type regionalUpstreamTransport struct {
	service     *httpUpstreamService
	accountID   int64
	concurrency int
	profile     service.HTTPUpstreamProfile
	fingerprint *tlsfingerprint.Profile
}

func (t *regionalUpstreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.service.validateRequestHost(req); err != nil {
		return nil, err
	}
	proxy, region := t.service.upstreamRoutes.MatchForAccount(req.URL, t.accountID)
	req, bindErr := service.BindOpenAIRequestTimezone(req, proxy)
	if bindErr != nil {
		return nil, bindErr
	}
	var entry *upstreamClientEntry
	var err error
	if t.fingerprint != nil && req.URL.Scheme == "https" {
		entry, err = t.service.acquireRequestClientWithTLS(req, proxy, t.accountID, t.concurrency, t.fingerprint, t.profile)
	} else {
		entry, err = t.service.acquireRequestClient(req, proxy, t.accountID, t.concurrency, t.profile)
	}
	if err != nil {
		return nil, upstreamroute.TransportError(err)
	}
	finished := func() {
		atomic.AddInt64(&entry.inFlight, -1)
		atomic.StoreInt64(&entry.lastUsed, time.Now().UnixNano())
	}
	start := time.Now()
	resp, err := entry.client.Transport.RoundTrip(req)
	if region != "" {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		logger.FromContext(req.Context()).Info("upstream.region_route",
			zap.Int64("account_id", t.accountID), zap.String("upstream_host", req.URL.Hostname()),
			zap.String("region", region), zap.Int("status_code", status), zap.Bool("transport_error", err != nil),
			zap.Int64("response_header_ms", time.Since(start).Milliseconds()))
	}
	if err != nil {
		t.service.recordOpenAIHTTP2Failure(t.profile, entry.protocolMode, entry.proxyKey, err)
		finished()
		if region != "" {
			return nil, upstreamroute.TransportError(err)
		}
		return nil, err
	}
	t.service.recordOpenAIHTTP2Success(t.profile, entry.protocolMode, entry.proxyKey)
	resp.Body = wrapTrackedBody(resp.Body, finished)
	return resp, nil
}
