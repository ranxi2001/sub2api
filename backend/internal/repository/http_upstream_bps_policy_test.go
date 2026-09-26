package repository

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/util/transportdiag"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"
)

type bpsPolicyConn struct {
	net.Conn
	proto string
}

func (c bpsPolicyConn) ConnectionState() tls.ConnectionState {
	return tls.ConnectionState{NegotiatedProtocol: c.proto}
}
func bpsPolicyTrace(proto string) *transportdiag.Trace {
	x := &transportdiag.Trace{}
	r, _ := http.NewRequest("POST", "https://example.test", nil)
	r = x.Request(r)
	tr := httptrace.ContextClientTrace(r.Context())
	tr.GetConn("example.test")
	if proto != "" {
		tr.GotConn(httptrace.GotConnInfo{Conn: bpsPolicyConn{proto: proto}})
	}
	return x
}
func TestBPSFallbackBoundariesAndExpiry(t *testing.T) {
	proxy := "http://private:credential@127.0.0.1:19178"
	for _, tc := range []struct {
		name, proto, proxy string
		err                error
		cancel, expected   bool
	}{
		{"h2_eof", "h2", proxy, io.ErrUnexpectedEOF, false, true},
		{"tls_eof", "", proxy, io.ErrUnexpectedEOF, false, false},
		{"h1_eof", "http/1.1", proxy, io.ErrUnexpectedEOF, false, false},
		{"direct", "h2", directProxyKey, io.ErrUnexpectedEOF, false, false},
		{"cancelled", "h2", proxy, io.ErrUnexpectedEOF, true, false},
		{"deadline", "h2", proxy, context.DeadlineExceeded, false, false},
		{"application", "h2", proxy, errors.New("rejected"), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
			require.True(t, ok)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			svc.recordBPSHTTP2Failure(ctx, tc.proxy, bpsPolicyTrace(tc.proto), tc.err)
			require.Equal(t, tc.expected, svc.bpsHTTP1Active(tc.proxy, time.Now()))
			require.False(t, svc.bpsHTTP1Active("http://127.0.0.1:19000", time.Now()))
			require.False(t, svc.isOpenAIHTTP2FallbackActive(tc.proxy), "Codex fallback state must remain isolated")
			if tc.expected {
				key := sha256.Sum256([]byte(tc.proxy))
				svc.mu.Lock()
				svc.bpsHTTP2Fallbacks[key] = time.Now().Add(-time.Second)
				svc.mu.Unlock()
				require.Equal(t, upstreamProtocolModeBPSH2, svc.resolveProtocolMode(service.HTTPUpstreamProfileExcelBPS, tc.proxy, nil))
			}
		})
	}
}

type bpsErrorBody struct{ err error }

func (b bpsErrorBody) Read([]byte) (int, error) { return 0, b.err }
func (b bpsErrorBody) Close() error             { return nil }
func TestBPSFeedbackBodyDoesNotTreatNormalEOFAsFailure(t *testing.T) {
	calls := 0
	b := &bpsFeedbackBody{ReadCloser: io.NopCloser(strings.NewReader("done")), failed: func(error) { calls++ }}
	_, err := io.ReadAll(b)
	require.NoError(t, err)
	require.Zero(t, calls)
	require.NoError(t, b.Close())
	b = &bpsFeedbackBody{ReadCloser: bpsErrorBody{io.ErrUnexpectedEOF}, failed: func(error) { calls++ }}
	_, _ = b.Read(make([]byte, 1))
	_, _ = b.Read(make([]byte, 1))
	require.Equal(t, 1, calls)
	require.NoError(t, b.Close())
}
