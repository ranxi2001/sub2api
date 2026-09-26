package transportdiag

import (
	"crypto/tls"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
)

// Trace collects allowlisted transport evidence. No headers, addresses or raw
// errors are retained. Flags accumulate across internal reconnects: a later TLS
// failure must never erase evidence that an earlier connection could send.
type Trace struct {
	started, connected, reused, tlsStarted, tlsCompleted    atomic.Bool
	wroteHeaders, wroteRequest, firstByte, bodyRead, handed atomic.Bool
	protocol                                                atomic.Int32
	idleMillis                                              atomic.Int64
	tlsError                                                atomic.Value
}

func (t *Trace) Request(req *http.Request) *http.Request {
	trace := &httptrace.ClientTrace{
		GetConn:           func(string) { t.started.Store(true) },
		TLSHandshakeStart: func() { t.tlsStarted.Store(true) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				t.tlsCompleted.Store(true)
			} else {
				t.tlsError.Store(Classify(err))
			}
		},
		GotConn: func(i httptrace.GotConnInfo) {
			t.connected.Store(true)
			t.handed.Store(true)
			t.reused.Store(i.Reused)
			t.idleMillis.Store(i.IdleTime.Milliseconds())
			// Inspect the connection actually handed to HTTP, including reused TLS
			// connections for which TLSHandshakeDone is not emitted on this request.
			if conn, ok := i.Conn.(interface{ ConnectionState() tls.ConnectionState }); ok {
				switch conn.ConnectionState().NegotiatedProtocol {
				case "h2":
					t.protocol.Store(2)
				case "http/1.1", "":
					t.protocol.Store(1)
				}
			}
		},
		WroteHeaderField: func(string, []string) { t.handed.Store(true) },
		WroteHeaders:     func() { t.handed.Store(true); t.wroteHeaders.Store(true) },
		WroteRequest: func(i httptrace.WroteRequestInfo) {
			t.handed.Store(true)
			if i.Err == nil {
				t.wroteRequest.Store(true)
			}
		},
		GotFirstResponseByte: func() { t.handed.Store(true); t.firstByte.Store(true) },
	}
	return req.Clone(httptrace.WithClientTrace(req.Context(), trace))
}

func (t *Trace) MarkBodyRead()          { t.bodyRead.Store(true); t.handed.Store(true) }
func (t *Trace) DefinitelyUnsent() bool { return t.started.Load() && !t.handed.Load() }
func (t *Trace) NegotiatedHTTP2() bool  { return t.connected.Load() && t.protocol.Load() == 2 }
func (t *Trace) Snapshot() map[string]any {
	phase := "unobserved"
	switch {
	case t.firstByte.Load():
		phase = "response_headers"
	case t.wroteRequest.Load():
		phase = "awaiting_response_headers"
	case t.wroteHeaders.Load() || t.bodyRead.Load():
		phase = "request_write"
	case t.connected.Load():
		phase = "connection_ready"
	case t.tlsCompleted.Load():
		phase = "tls_complete"
	case t.tlsStarted.Load():
		phase = "tls_handshake"
	case t.started.Load():
		phase = "connect"
	}
	protocol := "unknown"
	switch t.protocol.Load() {
	case 1:
		protocol = "http/1.1"
	case 2:
		protocol = "h2"
	}
	tlsError, _ := t.tlsError.Load().(string)
	return map[string]any{
		"phase": phase, "protocol": protocol, "connection_obtained": t.connected.Load(),
		"connection_reused": t.reused.Load(), "idle_ms": t.idleMillis.Load(),
		"tls_started": t.tlsStarted.Load(), "tls_completed": t.tlsCompleted.Load(), "tls_error_kind": tlsError,
		"headers_written": t.wroteHeaders.Load(), "request_written": t.wroteRequest.Load(),
		"body_read": t.bodyRead.Load(), "first_response_byte": t.firstByte.Load(),
	}
}
