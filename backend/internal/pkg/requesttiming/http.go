package requesttiming

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/util/transportdiag"
)

type attemptKey struct{}
type Trace struct {
	c               *Collector
	index           int
	logicalResponse bool
}

func StartAttempt(req *http.Request, accountID, proxyID int64) (*http.Request, *Trace) {
	// A handshake may make an HTTP request inside a native WS response. Keep
	// that diagnostic child from replacing the logical response's ownership.
	parent := 0
	if tr, ok := req.Context().Value(attemptKey{}).(*Trace); ok && tr != nil && tr.c == From(req.Context()) {
		tr.update(func(a *Attempt) {
			if a.Route.Upstream == "websocket" && a.EndMS == nil {
				parent = a.Number
			}
		})
	}
	return startAttempt(req, accountID, proxyID, "egress", parent)
}

// StartTransport separates physical Client.Do calls, including the safe TLS
// retry, from the enclosing plugin/proxy egress attempt.
func StartTransport(req *http.Request) (*http.Request, *Trace) {
	parent, _ := req.Context().Value(attemptKey{}).(*Trace)
	if parent == nil {
		return req, nil
	}
	var accountID, proxyID int64
	parent.update(func(a *Attempt) { accountID = a.AccountID; proxyID = a.ProxyID })
	return startAttempt(req, accountID, proxyID, "http", parent.index+1)
}
func startAttempt(req *http.Request, accountID, proxyID int64, kind string, parent int) (*http.Request, *Trace) {
	c := From(req.Context())
	if c == nil {
		return req, nil
	}
	c.mu.Lock()
	if c.finished || len(c.data.Attempts) >= 32 {
		c.data.Truncated = true
		c.mu.Unlock()
		return req, nil
	}
	idx := len(c.data.Attempts)
	c.data.Attempts = append(c.data.Attempts, Attempt{Route: transportdiag.HTTPRoute(req), Kind: kind, Parent: parent, Number: idx + 1, AccountID: accountID, ProxyID: proxyID, StartMS: c.offset(c.now()), RequestBytes: req.ContentLength, Events: map[string]float64{}})
	if kind == "egress" && parent == 0 {
		c.data.FinalResponseAttempt = idx + 1
	}
	c.mu.Unlock()
	t := &Trace{c: c, index: idx}
	ctx := context.WithValue(req.Context(), attemptKey{}, t)
	return req.Clone(httptrace.WithClientTrace(ctx, t.clientTrace())), t
}
func (t *Trace) update(fn func(*Attempt)) {
	if t == nil {
		return
	}
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if !t.c.finished && (!t.logicalResponse || t.c.data.Attempts[t.index].EndMS == nil) {
		fn(&t.c.data.Attempts[t.index])
	}
}
func (t *Trace) mark(name string) {
	t.update(func(a *Attempt) { a.Events[name] = t.c.offset(t.c.now()) })
}
func (t *Trace) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(string) { t.mark("connection_start") },
		GotConn: func(i httptrace.GotConnInfo) {
			t.update(func(a *Attempt) { v := i.Reused; a.Reused = &v; a.Events["connection_ready"] = t.c.offset(t.c.now()) })
		},
		DNSStart: func(httptrace.DNSStartInfo) { t.mark("dns_start") }, DNSDone: func(httptrace.DNSDoneInfo) { t.mark("dns_end") },
		ConnectStart: func(string, string) { t.mark("tcp_start") }, ConnectDone: func(string, string, error) { t.mark("tcp_end") },
		TLSHandshakeStart: func() { t.mark("tls_start") }, TLSHandshakeDone: func(tls.ConnectionState, error) { t.mark("tls_end") },
		WroteHeaders: func() { t.mark("headers_written") }, WroteRequest: func(i httptrace.WroteRequestInfo) {
			if i.Err == nil {
				t.mark("request_written")
			} else {
				t.mark("request_write_failed")
			}
		},
		GotFirstResponseByte: func() { t.mark("first_byte") },
	}
}

// Headers marks Client.Do returning complete headers, before any body wrapper
// (notably gzip initialization) can wait for body bytes. Response preserves it.
func (t *Trace) Headers(resp *http.Response) {
	if resp == nil {
		return
	}
	t.update(func(a *Attempt) {
		if _, ok := a.Events["response_headers"]; !ok {
			a.Events["response_headers"] = t.c.offset(t.c.now())
		}
	})
}
func (t *Trace) Response(resp *http.Response, err error) {
	if t == nil {
		return
	}
	t.update(func(a *Attempt) {
		if err != nil {
			a.Error = errorKind(err)
		}
		if resp != nil {
			a.Status = resp.StatusCode
			a.Route.ResponseFormat = transportdiag.ResponseFormat(resp.Header)
			if _, ok := a.Events["response_headers"]; !ok {
				a.Events["response_headers"] = t.c.offset(t.c.now())
			}
		}
		if err != nil || resp == nil || resp.Body == nil {
			v := t.c.offset(t.c.now())
			a.EndMS = &v
		}
	})
	if resp != nil && resp.Body != nil {
		resp.Body = &responseBody{ReadCloser: resp.Body, trace: t}
	}
}
func errorKind(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return "transport_error"
}

type responseBody struct {
	closing atomic.Bool
	io.ReadCloser
	trace *Trace
	once  sync.Once
}

func (b *responseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.trace.update(func(a *Attempt) {
		a.ResponseBytes += int64(n)
		if err == io.EOF {
			a.BodyEOF = true
		} else if err != nil {
			// The SSE parser intentionally stops at a successful terminal event.
			// Closing its body can cancel the read pump; that is cleanup, not
			// a failed upstream attempt. Preserve cancellations before Close,
			// and any non-successful terminal or other transport error.
			if errors.Is(err, context.Canceled) && b.closing.Load() && a.Terminal == "completed" {
				a.CleanupCanceled = true
			} else {
				a.Error = errorKind(err)
			}
		}
	})
	if err != nil {
		b.end()
	}
	return n, err
}
func (b *responseBody) end() {
	b.once.Do(func() { b.trace.update(func(a *Attempt) { v := b.trace.c.offset(b.trace.c.now()); a.EndMS = &v }) })
}
func (b *responseBody) Close() error {
	b.closing.Store(true)
	err := b.ReadCloser.Close()
	b.end()
	return err
}

// ResponseContext links parser observations to their actual egress attempt.
func ResponseContext(ctx context.Context, resp *http.Response) context.Context {
	if resp != nil {
		if resp.Request != nil {
			if tr, ok := resp.Request.Context().Value(attemptKey{}).(*Trace); ok {
				return context.WithValue(ctx, attemptKey{}, tr)
			}
		}
		if body, ok := resp.Body.(*responseBody); ok {
			return context.WithValue(ctx, attemptKey{}, body.trace)
		}
	}
	return ctx
}

// Stage records the first observation at a streaming boundary, scoped to the
// physical attempt and its parents. Missing boundaries stay absent.
func Stage(ctx context.Context, name string) {
	t, _ := ctx.Value(attemptKey{}).(*Trace)
	if t == nil {
		return
	}
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if t.c.finished {
		return
	}
	if t.logicalResponse && t.c.data.Attempts[t.index].EndMS != nil {
		return
	}
	now := t.c.offset(t.c.now())
	for index := t.index; index >= 0; {
		a := &t.c.data.Attempts[index]
		if _, ok := a.Events[name]; !ok {
			a.Events[name] = now
		}
		index = a.Parent - 1
	}
}

type stageBody struct {
	io.ReadCloser
	ctx  context.Context
	name string
	once sync.Once
}

func (b *stageBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.once.Do(func() { Stage(b.ctx, b.name) })
	}
	return n, err
}
func WrapStage(ctx context.Context, body io.ReadCloser, name string) io.ReadCloser {
	if body == nil || From(ctx) == nil {
		return body
	}
	return &stageBody{ReadCloser: body, ctx: ctx, name: name}
}

// Encoding is observed before manual decompression removes response headers.
// net/http automatic decompression has already consumed the wire encoding.
func Encoding(ctx context.Context, resp *http.Response) {
	t, _ := ctx.Value(attemptKey{}).(*Trace)
	if t == nil || resp == nil {
		return
	}
	encoding := resp.Header.Get("Content-Encoding")
	if resp.Uncompressed {
		encoding = "gzip"
	}
	if encoding == "" {
		encoding = "identity"
	}
	if len(encoding) > 128 {
		encoding = encoding[:128]
	}
	t.update(func(a *Attempt) { a.ContentEncoding = encoding; a.AutoDecompressed = resp.Uncompressed })
}
