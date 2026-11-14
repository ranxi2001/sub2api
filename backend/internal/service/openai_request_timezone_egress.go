package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type requestTimezoneEgressKey struct{}

type requestTimezoneBinding struct {
	service *OpenAIGatewayService
	account *Account
}

type egressTimezoneEntry struct {
	name    string
	expires time.Time
}

// Cache by complete egress identity, including credentials used by rotating
// proxies. A different account proxy, ticket exit or fallback gets a new key.
type openAIRequestTimezoneEgress struct {
	mu      sync.Mutex
	entries map[[32]byte]egressTimezoneEntry
	flight  singleflight.Group
}

func (s *OpenAIGatewayService) withRequestTimezoneEgress(ctx context.Context, account *Account) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || account == nil || !account.IsOpenAI() {
		return ctx
	}
	return context.WithValue(ctx, requestTimezoneEgressKey{}, &requestTimezoneBinding{s, account})
}

func (b *requestTimezoneBinding) enabled(ctx context.Context) bool {
	return b != nil && b.service.settingService.GetOpenAIRequestTimezoneEnabled(ctx, false) &&
		b.service.settingService.GetOpenAIRequestTimezoneAuto(ctx, false)
}

func (s *OpenAIGatewayService) egressRequestTimezone(ctx context.Context, proxy string) string {
	if s.requestTimezoneProber == nil || ctx.Err() != nil {
		return ""
	}
	cache := &s.requestTimezoneEgress
	key := sha256.Sum256([]byte(proxy))
	lookup := func() (string, bool) {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		entry, ok := cache.entries[key]
		return entry.name, ok && time.Now().Before(entry.expires)
	}
	if name, ok := lookup(); ok {
		return name
	}
	result := cache.flight.DoChan(string(key[:]), func() (any, error) {
		if name, ok := lookup(); ok {
			return name, nil
		}
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		info, _, err := s.requestTimezoneProber.ProbeProxy(probeCtx, proxy)
		name := ""
		ttl := 15 * time.Second
		if err == nil && info != nil && net.ParseIP(info.IP) != nil && isAllowedOpenAIRequestTimezone(info.Timezone) {
			name = info.Timezone
			ttl = time.Minute
		}
		cache.mu.Lock()
		if cache.entries == nil {
			cache.entries = make(map[[32]byte]egressTimezoneEntry)
		}
		if len(cache.entries) >= 512 {
			// Evict expired entries first; bound memory even for short-lived exits.
			for k, v := range cache.entries {
				if time.Now().After(v.expires) {
					delete(cache.entries, k)
				}
			}
			if len(cache.entries) >= 512 {
				for k := range cache.entries {
					delete(cache.entries, k)
					break
				}
			}
		}
		cache.entries[key] = egressTimezoneEntry{name, time.Now().Add(ttl)}
		cache.mu.Unlock()
		return name, nil
	})
	select {
	case <-ctx.Done():
		return ""
	case r := <-result:
		name, _ := r.Val.(string)
		return name
	}
}

func rewriteRequestTimezoneHeaders(headers http.Header, timezone string) {
	// Only replace timezone headers already supplied by the client or account.
	// Do not invent protocol fields or change language preferences.
	for key := range headers {
		switch http.CanonicalHeaderKey(key) {
		case "Oai-Client-Timezone", "X-Openai-Timezone", "X-Timezone":
			headers[key] = []string{timezone}
		}
	}
}

// BindOpenAIRequestTimezone runs after the HTTP transport selects its actual
// proxy, including regional routing and runtime fallback. It never mutates the
// caller's request, so retries start from the original timezone values.
func BindOpenAIRequestTimezone(req *http.Request, proxy string) (*http.Request, error) {
	if req == nil {
		return req, nil
	}
	b, _ := req.Context().Value(requestTimezoneEgressKey{}).(*requestTimezoneBinding)
	if !b.enabled(req.Context()) {
		return req, nil
	}
	name := b.service.egressRequestTimezone(req.Context(), proxy)
	if name == "" {
		return req, nil
	}
	clone := req.Clone(req.Context())
	rewriteRequestTimezoneHeaders(clone.Header, name)
	// A rebuilt/explicitly preserved task already has a verified input and
	// tools snapshot. Keep that body at the physical transport boundary;
	// automatic egress timezone still applies to the header metadata.
	if openAIAgentRecoveryPreservesHistory(req.Context()) {
		return clone, nil
	}
	if req.GetBody == nil || (req.Header.Get("Content-Type") != "" && !strings.HasPrefix(strings.ToLower(req.Header.Get("Content-Type")), "application/json")) {
		return clone, nil
	}
	reader, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		return nil, err
	}
	body = normalizeOpenAIRequestLocaleTimezone(req.Context(), b.account, body, "http", name)
	clone.Body = &requestTimezoneBody{Reader: bytes.NewReader(body), original: req.Body}
	clone.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	clone.ContentLength = int64(len(body))
	return clone, nil
}

type requestTimezoneBody struct {
	io.Reader
	original io.Closer
}

func (b *requestTimezoneBody) Close() error {
	if b.original != nil {
		return b.original.Close()
	}
	return nil
}

// Keep the timezone headers alongside the existing client header allowlist only
// while timezone binding is enabled. No header is invented when none is sent.
func (s *OpenAIGatewayService) requestTimezoneHeaders(ctx context.Context, account *Account, source, target http.Header) {
	if s == nil || account == nil || !account.IsOpenAI() || !s.settingService.GetOpenAIRequestTimezoneEnabled(ctx, false) {
		return
	}
	for key, values := range source {
		switch http.CanonicalHeaderKey(key) {
		case "Oai-Client-Timezone", "X-Openai-Timezone", "X-Timezone":
			if target.Get(key) == "" {
				target[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
			}
		}
	}
	if !s.settingService.GetOpenAIRequestTimezoneAuto(ctx, false) {
		rewriteRequestTimezoneHeaders(target, account.OpenAIRequestTimezone())
	}
}
