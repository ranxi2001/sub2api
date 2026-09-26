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
const bpsHTTP2FallbackMaxIdle = time.Hour

type bpsHTTP2Fallback struct {
	expiresAt time.Time
	started   bool
}

func (s *httpUpstreamService) bpsHTTP1Active(proxyKey string, now time.Time) bool {
	key := sha256.Sum256([]byte(proxyKey))
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.bpsHTTP2Fallbacks[key]
	if ok && !now.Before(state.expiresAt) {
		delete(s.bpsHTTP2Fallbacks, key)
		return false
	}
	if ok && !state.started {
		// Node health can quarantine a broken stream for up to 30 minutes. Start
		// the short protocol trial only when a later request actually reuses it.
		state.started = true
		state.expiresAt = now.Add(bpsHTTP2FallbackTTL)
		s.bpsHTTP2Fallbacks[key] = state
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
	if state := s.bpsHTTP2Fallbacks[key]; now.Before(state.expiresAt) {
		s.mu.Unlock()
		return
	}
	if s.bpsHTTP2Fallbacks == nil {
		s.bpsHTTP2Fallbacks = make(map[[32]byte]bpsHTTP2Fallback)
	}
	for k, state := range s.bpsHTTP2Fallbacks {
		if !now.Before(state.expiresAt) {
			delete(s.bpsHTTP2Fallbacks, k)
		}
	}
	// Bound state even when proxy membership churns. Never evict active requests.
	if len(s.bpsHTTP2Fallbacks) >= defaultMaxUpstreamClients {
		s.mu.Unlock()
		return
	}
	s.bpsHTTP2Fallbacks[key] = bpsHTTP2Fallback{expiresAt: now.Add(bpsHTTP2FallbackMaxIdle)}
	s.mu.Unlock()
	slog.Warn("excel_bps.http2_fallback_activated", "proxy_hash", fmt.Sprintf("%x", key[:8]), "error_kind", kind, "duration_seconds", int(bpsHTTP2FallbackTTL.Seconds()), "transport", trace.Snapshot())
}

type bpsFeedbackBody struct {
	trace *transportdiag.Trace
	io.ReadCloser
	once   sync.Once
	failed func(error)
}

func (b *bpsFeedbackBody) Read(p []byte) (int, error) {
	if b.trace != nil {
		b.trace.MarkResponseBodyRead()
	}
	n, err := b.ReadCloser.Read(p)
	// Normal EOF includes successful SSE completion. Only a transport error is
	// feedback; protocol-level missing terminal events remain the bridge's job.
	if err != nil && !errors.Is(err, io.EOF) {
		b.once.Do(func() { b.failed(err) })
	}
	return n, err
}
