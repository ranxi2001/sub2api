package transportdiag

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"
)

type traceTestConn struct {
	net.Conn
	protocol string
}

func (c traceTestConn) ConnectionState() tls.ConnectionState {
	return tls.ConnectionState{NegotiatedProtocol: c.protocol}
}
func TestTraceSafetyAndStages(t *testing.T) {
	cases := []struct {
		name, phase string
		fire        func(*Trace, *httptrace.ClientTrace)
		safe        bool
	}{
		{"missing", "unobserved", func(*Trace, *httptrace.ClientTrace) {}, false},
		{"dial", "connect", func(_ *Trace, c *httptrace.ClientTrace) { c.GetConn("private-host") }, true},
		{"tls", "tls_handshake", func(_ *Trace, c *httptrace.ClientTrace) {
			c.GetConn("private-host")
			c.TLSHandshakeStart()
			c.TLSHandshakeDone(tls.ConnectionState{}, errors.New("tls: SECRET"))
		}, true},
		{"connection", "connection_ready", func(_ *Trace, c *httptrace.ClientTrace) {
			c.GetConn("private-host")
			c.GotConn(httptrace.GotConnInfo{Conn: traceTestConn{protocol: "h2"}, Reused: true, IdleTime: time.Second})
		}, false},
		{"body_without_trace", "request_write", func(x *Trace, _ *httptrace.ClientTrace) { x.MarkBodyRead() }, false},
		{"written", "awaiting_response_headers", func(_ *Trace, c *httptrace.ClientTrace) {
			c.GetConn("private-host")
			c.WroteHeaders()
			c.WroteRequest(httptrace.WroteRequestInfo{})
		}, false},
		{"first_byte", "response_headers", func(_ *Trace, c *httptrace.ClientTrace) { c.GotFirstResponseByte() }, false},
		{"reconnect_after_write", "awaiting_response_headers", func(_ *Trace, c *httptrace.ClientTrace) {
			c.WroteRequest(httptrace.WroteRequestInfo{})
			c.GetConn("private-host")
			c.TLSHandshakeStart()
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := &Trace{}
			req, _ := http.NewRequestWithContext(context.Background(), "POST", "https://example.test", nil)
			r := x.Request(req)
			tc.fire(x, httptrace.ContextClientTrace(r.Context()))
			if x.DefinitelyUnsent() != tc.safe {
				t.Fatalf("unsafe retry evidence: %+v", x.Snapshot())
			}
			if x.Snapshot()["phase"] != tc.phase {
				t.Fatalf("phase: %+v", x.Snapshot())
			}
			text := fmt.Sprint(x.Snapshot())
			if strings.Contains(text, "SECRET") || strings.Contains(text, "private-host") {
				t.Fatal("private data leaked")
			}
		})
	}
}
