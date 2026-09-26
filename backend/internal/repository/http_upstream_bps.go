package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/transportdiag"
)

// A short circuit breaker for a proven H2 connection failure, not a claim that
// the provider never supports H2. Future BPS requests keep the same proxy but
// use a separate H1 pool for one minute. No failed request is replayed here.
const bpsHTTP2FallbackTTL = time.Minute

func (s *httpUpstreamService) bpsHTTP1Active(proxyKey string, now time.Time) bool {
	key := sha256.Sum256([]byte(proxyKey))
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.bpsHTTP2Fallbacks[key]
	if ok && !now.Before(until) {
		delete(s.bpsHTTP2Fallbacks, key)
		return false
	}
	return ok
}

func (s *httpUpstreamService) recordBPSHTTP2Failure(ctx context.Context, proxyKey string, trace *transportdiag.Trace, err error) {
	if trace == nil || !trace.NegotiatedHTTP2() || ctx.Err() != nil || !isHTTPProxyKey(proxyKey) {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	kind := transportdiag.Classify(err)
	if kind != "unexpected_eof" && kind != "connection_reset" && kind != "http2_error" {
		return
	}
	key := sha256.Sum256([]byte(proxyKey))
	now := time.Now()
	s.mu.Lock()
	if until := s.bpsHTTP2Fallbacks[key]; now.Before(until) {
		s.mu.Unlock()
		return
	}
	if s.bpsHTTP2Fallbacks == nil {
		s.bpsHTTP2Fallbacks = make(map[[32]byte]time.Time)
	}
	for k, until := range s.bpsHTTP2Fallbacks {
		if !now.Before(until) {
			delete(s.bpsHTTP2Fallbacks, k)
		}
	}
	// Bound state even when proxy membership churns. Never evict active requests.
	if len(s.bpsHTTP2Fallbacks) >= defaultMaxUpstreamClients {
		s.mu.Unlock()
		return
	}
	s.bpsHTTP2Fallbacks[key] = now.Add(bpsHTTP2FallbackTTL)
	s.mu.Unlock()
	slog.Warn("excel_bps.http2_fallback_activated", "proxy_hash", fmt.Sprintf("%x", key[:8]), "error_kind", kind, "duration_seconds", int(bpsHTTP2FallbackTTL.Seconds()))
}

type bpsFeedbackBody struct {
	io.ReadCloser
	once   sync.Once
	failed func(error)
}

func (b *bpsFeedbackBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	// Normal EOF includes successful SSE completion. Only a transport error is
	// feedback; protocol-level missing terminal events remain the bridge's job.
	if err != nil && !errors.Is(err, io.EOF) {
		b.once.Do(func() { b.failed(err) })
	}
	return n, err
}
