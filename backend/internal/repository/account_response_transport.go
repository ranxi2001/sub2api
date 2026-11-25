package repository

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Record native HTTP inference status before handing response headers to the caller.
// Opted-in 429 samples are queued for asynchronous persistence.
type accountResponseTransport struct{ base http.RoundTripper }

func (t *accountResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	started := time.Now().UTC()
	resp, err := t.base.RoundTrip(req)
	if err == nil && resp != nil && req.Context().Err() == nil && isAccountInferenceRequest(req) {
		account, received := requestcapture.AccountFromContext(req.Context()), time.Now()
		// These statuses need the semantic error before they can be counted.
		// Observe the caller's normal reads; do not pre-read or delay headers.
		if resp.Body != nil && (resp.StatusCode == 400 || resp.StatusCode == 422 || resp.StatusCode == 502) {
			resp.Body = &accountErrorResponseBody{ReadCloser: resp.Body, ctx: req.Context(), account: account,
				status: resp.StatusCode, received: received, store: upstreamstats.Default}
		} else if resp.StatusCode != http.StatusRequestEntityTooLarge {
			// 413 is itself an explicit request-size rejection. Some gateway
			// handlers intentionally close its body without reading it.
			upstreamstats.Default.Record(account, resp.StatusCode, received)
		}
		service.ObserveOAuth429HTTP(req, requestcapture.AccountFromContext(req.Context()), resp.StatusCode, started)
	}
	return resp, err
}

// Capture at most 64 KiB of an error already being read by the gateway. An
// unread, incomplete, oversized or malformed error stays a provider failure.
// Bodies and their excerpts never enter the evidence store.
type accountErrorResponseBody struct {
	io.ReadCloser
	ctx       context.Context
	account   int64
	status    int
	received  time.Time
	store     *upstreamstats.Store
	once      sync.Once
	mu        sync.Mutex
	prefix    []byte
	complete  bool
	oversized bool
}

func (b *accountErrorResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	const limit = 64 * 1024
	b.mu.Lock()
	if len(b.prefix)+n <= limit {
		b.prefix = append(b.prefix, p[:n]...)
	} else {
		b.oversized = true
	}
	if err != nil {
		b.complete = err == io.EOF
	}
	b.mu.Unlock()
	if err != nil {
		b.observe()
	}
	return n, err
}

func (b *accountErrorResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.observe()
	return err
}

func (b *accountErrorResponseBody) observe() {
	b.once.Do(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.ctx.Err() == nil && b.complete && !b.oversized && service.IsUpstreamContextRejection(b.status, b.prefix) {
			return
		}
		b.store.Record(b.account, b.status, b.received)
	})
}

func isAccountInferenceRequest(req *http.Request) bool {
	if req.Method != http.MethodPost || req.URL == nil {
		return false
	}
	path := strings.TrimRight(req.URL.Path, "/")
	for _, suffix := range []string{"/messages", "/responses", "/responses/compact", "/chat/completions", "/completions", ":generateContent", ":streamGenerateContent"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}
