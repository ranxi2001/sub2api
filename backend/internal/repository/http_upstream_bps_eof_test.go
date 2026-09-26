package repository

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type bpsEOFConnectionKey struct{}

func TestBPSHTTP2PrematureCloseFallsBackWithoutReplaying(t *testing.T) {
	for _, afterHeaders := range []bool{false, true} {
		name := "before_headers"
		if afterHeaders {
			name = "during_body"
		}
		t.Run(name, func(t *testing.T) {
			var h2Calls, h1Calls atomic.Int32
			closePeer := make(chan struct{})
			release := sync.OnceFunc(func() { close(closePeer) })
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				_ = r.Body.Close()
				if r.ProtoMajor == 2 {
					h2Calls.Add(1)
					if afterHeaders {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("data: started\n\n"))
						flusher, ok := w.(http.Flusher)
						if !ok {
							t.Error("missing Flusher")
							return
						}
						flusher.Flush()
						<-closePeer
					}
					conn, ok := r.Context().Value(bpsEOFConnectionKey{}).(net.Conn)
					if !ok {
						t.Error("missing connection")
						return
					}
					_ = conn.Close()
					return
				}
				h1Calls.Add(1)
				_, _ = w.Write([]byte("ok"))
			}))
			target.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
				return context.WithValue(ctx, bpsEOFConnectionKey{}, c)
			}
			target.EnableHTTP2 = true
			target.StartTLS()
			t.Cleanup(target.Close)
			t.Cleanup(release)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodConnect {
					w.WriteHeader(405)
					return
				}
				upstream, err := net.Dial("tcp", strings.TrimPrefix(target.URL, "https://"))
				if err != nil {
					w.WriteHeader(502)
					return
				}
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					_ = upstream.Close()
					t.Error("missing Hijacker")
					return
				}
				downstream, buf, err := hijacker.Hijack()
				if err != nil {
					_ = upstream.Close()
					return
				}
				if _, err = buf.WriteString("HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
					_ = upstream.Close()
					_ = downstream.Close()
					return
				}
				if err = buf.Flush(); err != nil {
					_ = upstream.Close()
					_ = downstream.Close()
					return
				}
				go func() { defer func() { _ = upstream.Close(); _ = downstream.Close() }(); _, _ = io.Copy(upstream, buf) }()
				go func() {
					defer func() { _ = upstream.Close(); _ = downstream.Close() }()
					_, _ = io.Copy(downstream, upstream)
				}()
			}))
			t.Cleanup(proxy.Close)
			svc, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
			require.True(t, ok)
			// Run unchanged on production baseline, which routed BPS as long_stream.
			profile := service.HTTPUpstreamProfile("excel_bps")
			ctx := service.WithHTTPUpstreamProfile(t.Context(), profile)
			if service.HTTPUpstreamProfileFromContext(ctx) != profile {
				profile = service.HTTPUpstreamProfileLongStream
				ctx = service.WithHTTPUpstreamProfile(t.Context(), profile)
			}
			trust := func() *upstreamClientEntry {
				entry, err := svc.getClientEntry(proxy.URL, 300, 4, profile, false, false)
				require.NoError(t, err)
				transport, ok := entry.client.Transport.(*http.Transport)
				require.True(t, ok)
				roots := x509.NewCertPool()
				roots.AddCert(target.Certificate())
				transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
				t.Cleanup(transport.CloseIdleConnections)
				return entry
			}
			trust()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, strings.NewReader("synthetic request"))
			require.NoError(t, err)
			resp, err := svc.Do(req, proxy.URL, 300, 4)
			if afterHeaders {
				require.NoError(t, err)
				release()
				_, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
			require.Error(t, err)
			t.Logf("simulated H2 close: %T %v", err, err)
			require.EqualValues(t, 1, h2Calls.Load(), "failed POST must never replay")
			require.Zero(t, h1Calls.Load())
			entry := trust()
			require.Equal(t, "bps_h1", entry.protocolMode, "only next independent request uses H1")
			next, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, strings.NewReader("independent request"))
			require.NoError(t, err)
			resp, err = svc.Do(next, proxy.URL, 300, 4)
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, "ok", string(body))
			require.EqualValues(t, 1, h2Calls.Load())
			require.EqualValues(t, 1, h1Calls.Load())
			require.Equal(t, upstreamProtocolModeLongStreamH2, svc.resolveProtocolMode(service.HTTPUpstreamProfileLongStream, proxy.URL, nil))
		})
	}
}
