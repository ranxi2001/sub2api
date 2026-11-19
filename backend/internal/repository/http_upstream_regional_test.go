package repository

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamroute"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func regionalHTTPConfig(t *testing.T, proxy, domain string) *config.Config {
	t.Helper()
	t.Setenv("TEST_HTTP_REGION_PROXY", proxy)
	return &config.Config{Gateway: config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccountProxy, UpstreamRouting: upstreamroute.Config{Enabled: true, Regions: []upstreamroute.Region{{ID: "us", ProxyURLEnv: "TEST_HTTP_REGION_PROXY"}}, Rules: []upstreamroute.Rule{{Domain: domain, Region: "us"}}}}}
}
func TestRegionalHTTPPreservesPayloadAndExplicitProxy(t *testing.T) {
	var us, explicit atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		us.Add(1)
		require.Equal(t, "api.vendor.test", r.URL.Host)
		require.Equal(t, "Bearer synthetic-upstream-key", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"stream":true}`, string(body))
		w.WriteHeader(201)
	}))
	defer proxy.Close()
	accountProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { explicit.Add(1); w.WriteHeader(202) }))
	defer accountProxy.Close()
	cfg := regionalHTTPConfig(t, proxy.URL, "api.vendor.test")
	up := NewHTTPUpstream(cfg)
	for _, p := range []string{"", accountProxy.URL} {
		req, err := http.NewRequestWithContext(t.Context(), "POST", "http://api.vendor.test/v1/responses", strings.NewReader(`{"stream":true}`))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer synthetic-upstream-key")
		resp, err := up.Do(req, p, 11, 2)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	require.EqualValues(t, 1, us.Load())
	require.EqualValues(t, 1, explicit.Load())
}

func TestRegionalHTTPCompressesConfiguredUpstream(t *testing.T) {
	const origin = "http://api.vendor.test"
	payload := `{"input":"` + strings.Repeat("regional prompt ", 512) + `"}`
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") != "gzip" || r.ContentLength >= int64(len(payload)) {
			t.Error("regional egress did not receive the compressed request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		decoder, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer func() { require.NoError(t, decoder.Close()) }()
		got, err := io.ReadAll(decoder)
		if err != nil || string(got) != payload {
			t.Errorf("decoded regional request differs from the original: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(proxy.Close)
	cfg := regionalHTTPConfig(t, proxy.URL, "api.vendor.test")
	cfg.Gateway.UpstreamRequestCompression = []config.UpstreamRequestCompressionRule{{Origin: origin, Encoding: "gzip"}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, origin+"/v1/responses", strings.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := NewHTTPUpstream(cfg).Do(req, "", 11, 2)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestRegionalHTTPAccountScope(t *testing.T) {
	var direct, regional, explicit atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		direct.Add(1)
		w.WriteHeader(204)
	}))
	defer upstream.Close()
	regionalProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		regional.Add(1)
		w.WriteHeader(201)
	}))
	defer regionalProxy.Close()
	accountProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		explicit.Add(1)
		w.WriteHeader(202)
	}))
	defer accountProxy.Close()
	cfg := regionalHTTPConfig(t, regionalProxy.URL, "127.0.0.1")
	cfg.Gateway.UpstreamRouting.Rules[0].AccountIDs = []int64{101}
	up := NewHTTPUpstream(cfg)
	for _, tc := range []struct {
		accountID int64
		proxy     string
		status    int
	}{{101, "", 201}, {102, "", 204}, {101, accountProxy.URL, 202}} {
		req, err := http.NewRequestWithContext(t.Context(), "GET", upstream.URL, nil)
		require.NoError(t, err)
		resp, err := up.Do(req, tc.proxy, tc.accountID, 2)
		require.NoError(t, err)
		require.Equal(t, tc.status, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}
	require.EqualValues(t, 1, direct.Load())
	require.EqualValues(t, 1, regional.Load())
	require.EqualValues(t, 1, explicit.Load())
}
func TestRegionalHTTPRedirectChangesEgressAndStripsCredentials(t *testing.T) {
	var us, eu atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		us.Add(1)
		require.Equal(t, "api.us.test", r.URL.Host)
		w.Header().Set("Location", "http://api.eu.test/final")
		w.WriteHeader(302)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eu.Add(1)
		require.Equal(t, "api.eu.test", r.URL.Host)
		require.Empty(t, r.Header.Get("Authorization"))
		w.WriteHeader(204)
	}))
	defer second.Close()
	cfg := regionalHTTPConfig(t, first.URL, "api.us.test")
	t.Setenv("TEST_HTTP_EU_PROXY", second.URL)
	cfg.Gateway.UpstreamRouting.Regions = append(cfg.Gateway.UpstreamRouting.Regions, upstreamroute.Region{ID: "eu", ProxyURLEnv: "TEST_HTTP_EU_PROXY"})
	cfg.Gateway.UpstreamRouting.Rules = append(cfg.Gateway.UpstreamRouting.Rules, upstreamroute.Rule{Domain: "api.eu.test", Region: "eu"})
	up := NewHTTPUpstream(cfg)
	req, err := http.NewRequestWithContext(t.Context(), "GET", "http://api.us.test/start", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer must-not-leak")
	resp, err := up.Do(req, "", 12, 2)
	require.NoError(t, err)
	require.Equal(t, 204, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	require.EqualValues(t, 1, us.Load())
	require.EqualValues(t, 1, eu.Load())
	req = req.WithContext(service.WithHTTPUpstreamRedirectsDisabled(t.Context()))
	resp, err = up.Do(req, "", 12, 2)
	require.NoError(t, err)
	require.Equal(t, 302, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	require.EqualValues(t, 1, eu.Load())
}
func TestRegionalHTTPMissAndSpecialPathsRemainDirect(t *testing.T) {
	var direct, egress atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct.Add(1); w.WriteHeader(204) }))
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { egress.Add(1); w.WriteHeader(201) }))
	defer proxy.Close()
	cfg := regionalHTTPConfig(t, proxy.URL, "127.0.0.1")
	up := NewHTTPUpstream(cfg)
	for _, profile := range []service.HTTPUpstreamProfile{service.HTTPUpstreamProfileOpenAIHarvest, service.HTTPUpstreamProfileExcelBPS} {
		req, err := http.NewRequestWithContext(service.WithHTTPUpstreamProfile(t.Context(), profile), "GET", upstream.URL, nil)
		require.NoError(t, err)
		resp, err := up.Do(req, "", 15, 2)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	cfg.Gateway.UpstreamRouting.Rules[0].Domain = "unmatched.test"
	up = NewHTTPUpstream(cfg)
	req, err := http.NewRequestWithContext(t.Context(), "GET", upstream.URL, nil)
	require.NoError(t, err)
	resp, err := up.Do(req, "", 15, 2)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.EqualValues(t, 3, direct.Load())
	require.Zero(t, egress.Load())
}
func TestRegionalHTTPFailureDoesNotReplayOrGoDirect(t *testing.T) {
	var direct, received atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct.Add(1); w.WriteHeader(204) }))
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		_, err := io.Copy(io.Discard, r.Body)
		require.NoError(t, err)
		h, ok := w.(http.Hijacker)
		require.True(t, ok)
		conn, _, err := h.Hijack()
		require.NoError(t, err)
		_ = conn.Close()
	}))
	defer proxy.Close()
	cfg := regionalHTTPConfig(t, proxy.URL, "127.0.0.1")
	up := NewHTTPUpstream(cfg)
	req, err := http.NewRequestWithContext(t.Context(), "POST", upstream.URL, strings.NewReader("synthetic request"))
	require.NoError(t, err)
	_, err = up.Do(req, "", 16, 2)
	require.Error(t, err)
	require.EqualValues(t, 1, received.Load())
	require.Zero(t, direct.Load())
	t.Setenv("TEST_HTTP_REGION_PROXY", "http://user:private-secret@127.0.0.1:1")
	up = NewHTTPUpstream(cfg)
	_, err = up.Do(req, "", 16, 2)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-secret")
	require.Zero(t, direct.Load())
}
func TestRegionalSSEFlushCancellationAndPoolRelease(t *testing.T) {
	cancelled := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		f, ok := w.(http.Flusher)
		require.True(t, ok)
		f.Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer proxy.Close()
	cfg := regionalHTTPConfig(t, proxy.URL, "api.stream.test")
	svc, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
	require.True(t, ok)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://api.stream.test/v1/responses", strings.NewReader("{}"))
	require.NoError(t, err)
	resp, err := svc.Do(req, "", 17, 2)
	require.NoError(t, err)
	chunk := make([]byte, len("data: first\n\n"))
	_, err = io.ReadFull(resp.Body, chunk)
	require.NoError(t, err)
	require.Equal(t, "data: first\n\n", string(chunk))
	require.NoError(t, resp.Body.Close())
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("SSE cancellation did not reach egress")
	}
	for _, entry := range svc.clients {
		require.Zero(t, atomic.LoadInt64(&entry.inFlight))
	}
}
func TestRegionalSOCKSAndPlainHTTPFingerprint(t *testing.T) {
	proxy, calls := startTestSOCKS5Proxy(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer upstream.Close()
	cfg := regionalHTTPConfig(t, proxy, "127.0.0.1")
	up := NewHTTPUpstream(cfg)
	req, err := http.NewRequestWithContext(t.Context(), "GET", upstream.URL, nil)
	require.NoError(t, err)
	resp, err := up.DoWithTLS(req, "", 18, 2, &tlsfingerprint.Profile{Name: "http-has-no-TLS"})
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.EqualValues(t, 1, calls.Load())
}
func TestRegionalHTTPSUsesCONNECTAndTLSVerification(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(204) }))
	defer target.Close()
	tunnel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "CONNECT", r.Method)
		require.Equal(t, "api.secure.test:443", r.Host)
		u, err := url.Parse(target.URL)
		require.NoError(t, err)
		remote, err := net.Dial("tcp", u.Host)
		require.NoError(t, err)
		h, ok := w.(http.Hijacker)
		require.True(t, ok)
		client, buffer, err := h.Hijack()
		require.NoError(t, err)
		defer func() { _ = client.Close() }()
		defer func() { _ = remote.Close() }()
		_, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		require.NoError(t, err)
		require.NoError(t, buffer.Flush())
		go func() { _, _ = io.Copy(remote, client); _ = remote.Close() }()
		_, _ = io.Copy(client, remote)
	}))
	defer tunnel.Close()
	cfg := regionalHTTPConfig(t, tunnel.URL, "api.secure.test")
	up := NewHTTPUpstream(cfg)
	req, err := http.NewRequestWithContext(t.Context(), "GET", "https://api.secure.test/", nil)
	require.NoError(t, err)
	_, err = up.Do(req, "", 19, 2)
	require.Error(t, err, "untrusted upstream TLS certificate must still fail")
	require.Zero(t, hits.Load())
	_, err = up.DoWithTLS(req, "", 19, 2, &tlsfingerprint.Profile{Name: "test"})
	require.Error(t, err)
	require.Zero(t, hits.Load())
}

func TestRegionalHTTPSCONNECTSuccessPreservesUpstreamTLS(t *testing.T) {
	var hits, connectHits atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		require.Equal(t, "Bearer synthetic", r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("Proxy-Authorization"))
		w.WriteHeader(204)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectHits.Add(1)
		require.Equal(t, "CONNECT", r.Method)
		require.Equal(t, target.Host, r.Host)
		remote, err := net.Dial("tcp", target.Host)
		require.NoError(t, err)
		h, ok := w.(http.Hijacker)
		require.True(t, ok)
		client, b, err := h.Hijack()
		require.NoError(t, err)
		defer func() { _ = client.Close() }()
		defer func() { _ = remote.Close() }()
		_, err = b.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		require.NoError(t, err)
		require.NoError(t, b.Flush())
		go func() { _, _ = io.Copy(remote, client); _ = remote.Close() }()
		_, _ = io.Copy(client, remote)
	}))
	defer proxy.Close()
	cfg := regionalHTTPConfig(t, proxy.URL, target.Hostname())
	svc, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
	require.True(t, ok)
	entry, err := svc.getOrCreateClient(proxy.URL, 20, 2)
	require.NoError(t, err)
	tr, ok := entry.client.Transport.(*http.Transport)
	require.True(t, ok)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	req, err := http.NewRequestWithContext(t.Context(), "POST", upstream.URL, strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer synthetic")
	resp, err := svc.Do(req, "", 20, 2)
	require.NoError(t, err)
	require.Equal(t, 204, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	tr.CloseIdleConnections()
	require.EqualValues(t, 1, connectHits.Load())
	require.EqualValues(t, 1, hits.Load())
}

func TestRegionalEgressRetainsPublicHostValidationAndInvalidProxyFailure(t *testing.T) {
	var egressHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { egressHits.Add(1); w.WriteHeader(204) }))
	defer proxy.Close()
	cfg := regionalHTTPConfig(t, proxy.URL, "127.0.0.1")
	up := NewHTTPUpstream(cfg)
	req, err := http.NewRequestWithContext(service.WithHTTPUpstreamPublicHostsOnly(t.Context()), http.MethodGet, "http://127.0.0.1/v1/responses", nil)
	require.NoError(t, err)
	_, err = up.Do(req, "", 21, 2)
	require.Error(t, err, "a configured relay must not bypass the request's public-host restriction")
	require.Zero(t, egressHits.Load())
	req = req.WithContext(t.Context())
	_, err = up.Do(req, "://bad-account-proxy", 21, 2)
	require.Error(t, err, "an invalid explicit account proxy must not be silently replaced")
	require.Zero(t, egressHits.Load())
	t.Setenv("TEST_HTTP_REGION_PROXY", "")
	up = NewHTTPUpstream(cfg)
	_, err = up.Do(req, "", 21, 2)
	require.Error(t, err, "bad regional configuration must fail closed even for programmatic construction")
	require.Zero(t, egressHits.Load())
}

func TestRegionalRoutingSurvivesAstraWrapper(t *testing.T) {
	calls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "api.vendor.test", r.URL.Host)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()
	cfg := regionalHTTPConfig(t, proxy.URL, "api.vendor.test")
	cfg.SetAstraRoutingLoader(func(context.Context) config.AstraRoutingSettings { return config.AstraRoutingSettings{} })
	up := NewHTTPUpstream(cfg)
	require.IsType(t, &astraRoutingUpstream{}, up)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://api.vendor.test/check", nil)
	require.NoError(t, err)
	resp, err := up.Do(req, "", 11, 1)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, 1, calls)
}
