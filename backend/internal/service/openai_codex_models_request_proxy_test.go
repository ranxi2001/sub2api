package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type codexModelsRequestProxyRepo struct {
	ProxyRepository
	proxies map[int64]*Proxy
}

func (r *codexModelsRequestProxyRepo) GetByID(_ context.Context, id int64) (*Proxy, error) {
	return r.proxies[id], nil
}

func codexModelsProxyRecord(t *testing.T, id int64, raw string) *Proxy {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	host, port, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	number, err := strconv.Atoi(port)
	require.NoError(t, err)
	return &Proxy{ID: id, Protocol: u.Scheme, Host: host, Port: number, Status: StatusActive}
}

func TestFetchCodexModelsManifestRequestProxyReplacesSingleAndIsolatesCache(t *testing.T) {
	var directCalls, singleCalls, selectedCalls, replacementCalls atomic.Int32
	server := func(name string, calls *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"slug":"` + name + `"}]}`))
		}))
	}
	origin := server("direct", &directCalls)
	defer origin.Close()
	single := server("single", &singleCalls)
	defer single.Close()
	selected := server("selected", &selectedCalls)
	defer selected.Close()
	replacement := server("replacement", &replacementCalls)
	defer replacement.Close()
	originalURL := chatgptCodexModelsURL
	chatgptCodexModelsURL = origin.URL
	t.Cleanup(func() { chatgptCodexModelsURL = originalURL })

	singleProxy := codexModelsProxyRecord(t, 10, single.URL)
	selectedProxy := codexModelsProxyRecord(t, 20, selected.URL)
	replacementProxy := codexModelsProxyRecord(t, 30, replacement.URL)
	repo := &codexModelsRequestProxyRepo{proxies: map[int64]*Proxy{10: singleProxy, 20: selectedProxy, 30: replacementProxy}}
	svc := &OpenAIGatewayService{proxyRepo: repo}
	account := newCodexModelsTestAccount()
	account.ProxyID, account.Proxy = &singleProxy.ID, singleProxy
	fetch := func(want string) {
		t.Helper()
		manifest, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.137.0", "")
		require.NoError(t, err)
		require.JSONEq(t, `{"models":[{"slug":"`+want+`"}]}`, string(manifest.Body))
	}
	fetch("single")
	account.Extra = map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []int64{20}}
	fetch("selected")
	fetch("selected")
	account.Extra["openai_proxy_ids"] = []int64{30}
	fetch("replacement")
	require.Equal(t, int32(0), directCalls.Load())
	require.Equal(t, int32(1), singleCalls.Load())
	require.Equal(t, int32(1), selectedCalls.Load())
	require.Equal(t, int32(1), replacementCalls.Load())
	require.Equal(t, int64(10), *account.ProxyID)

	// A previously cached success cannot hide a now-unavailable saved group.
	replacementProxy.Status = StatusDisabled
	_, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.137.0", "")
	require.ErrorContains(t, err, "direct fallback is disabled")
	require.Equal(t, int32(0), directCalls.Load())
	require.Equal(t, int32(1), singleCalls.Load())
	require.Equal(t, int32(1), replacementCalls.Load())
}

func TestFetchCodexModelsManifestRequestProxyUnavailableDoesNotSend(t *testing.T) {
	var directCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer origin.Close()
	originalURL := chatgptCodexModelsURL
	chatgptCodexModelsURL = origin.URL
	t.Cleanup(func() { chatgptCodexModelsURL = originalURL })
	account := newCodexModelsTestAccount()
	account.Extra = map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []int64{404}}
	svc := &OpenAIGatewayService{proxyRepo: &codexModelsRequestProxyRepo{}}
	_, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.137.0", "")
	require.ErrorContains(t, err, "direct fallback is disabled")
	require.Zero(t, directCalls.Load())
}

func TestFetchCodexModelsManifestRequestProxyOwnsTransportAndCachePolicy(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer proxy.Close()
	originalURL := chatgptCodexModelsURL
	chatgptCodexModelsURL = "http://models.fixture.invalid/backend-api/codex/models"
	t.Cleanup(func() { chatgptCodexModelsURL = originalURL })
	record := codexModelsProxyRecord(t, 20, proxy.URL)
	svc := &OpenAIGatewayService{proxyRepo: &codexModelsRequestProxyRepo{proxies: map[int64]*Proxy{20: record}}}
	account := newCodexModelsTestAccount()
	account.ProxyID, account.Proxy = &record.ID, record
	_, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.137.0", "")
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())

	manager := &PluginManager{}
	manager.route.Store(&pluginRoute{rolloutPercent: 100, unavailable: "fixture plugin unavailable"})
	svc.pluginManager = manager
	account.Extra = map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []int64{20}}
	_, err = svc.FetchCodexModelsManifest(context.Background(), account, "0.137.0", "")
	require.NoError(t, err, "request proxies keep the selected native transport")
	require.Equal(t, int32(2), calls.Load(), "the old single-mode cache cannot conceal a different transport policy")
}
