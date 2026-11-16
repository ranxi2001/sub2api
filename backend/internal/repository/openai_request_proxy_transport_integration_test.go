//go:build bps_request_integration

package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type requestProxyTransportRepo struct {
	service.AccountRepository
	account *service.Account
}

func (r *requestProxyTransportRepo) GetByID(context.Context, int64) (*service.Account, error) {
	return r.account, nil
}
func (*requestProxyTransportRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}

type requestProxyTransportTickets struct {
	service.AstraTicketLifecycle
	proxy string
	calls atomic.Int32
}

func (*requestProxyTransportTickets) UnifiedAstraTicketsEnabled(context.Context) bool { return true }
func (p *requestProxyTransportTickets) ApplyStoredAstraTarget(_ context.Context, h http.Header, _ int64, _ string) (string, func(), error) {
	p.calls.Add(1)
	h.Set("Cookie", "__oailb=outside-route")
	return p.proxy, func() {}, nil
}

func requestProxyTransportFixture(t *testing.T) (*service.Account, string, string, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	selectedHits, outsideHits := &atomic.Int32{}, &atomic.Int32{}
	makeProxy := func(hits *atomic.Int32) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_proxy\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
		}))
		t.Cleanup(server.Close)
		return server
	}
	selected, outside := makeProxy(selectedHits), makeProxy(outsideHits)
	restore := mihomo.InstallBPSRequestIntegrationProbe(func(context.Context, string) error { return nil })
	t.Cleanup(restore)
	mihomo.SetBPSStaticProxyRecords([]mihomo.BPSStaticProxy{{ID: 1, URL: outside.URL}, {ID: 2, URL: selected.URL}})
	mihomo.SetBPSStaticRequestGroups(map[int64][]int64{300: {2}})
	mihomo.WarmBPSPools(t.Context(), 0, 1)
	account := &service.Account{ID: 300, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Concurrency: 6, Credentials: map[string]any{"access_token": "fixture-token"}, Extra: map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []int64{2}}}
	return account, selected.URL, outside.URL, selectedHits, outsideHits
}

// The HTTPS boundary is translated only after Astra routing. Actual socket
// selection still runs through the native HTTP transport and local proxies.
func requestProxyNativeHTTPBoundary(t *testing.T, upstream service.HTTPUpstream, mutate func(string) string) (service.HTTPUpstream, *atomic.Int32) {
	t.Helper()
	originHits := &atomic.Int32{}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { originHits.Add(1); w.WriteHeader(http.StatusBadGateway) }))
	t.Cleanup(origin.Close)
	originURL, err := url.Parse(origin.URL)
	require.NoError(t, err)
	return gatewayPinDelegate{call: func(r *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
		local := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = "http"
		u.Host = originURL.Host
		local.URL = &u
		local.RequestURI = ""
		if mutate != nil {
			proxy = mutate(proxy)
		}
		return upstream.Do(local, proxy, accountID, concurrency)
	}}, originHits
}

func runRequestProxyTransportAccountTest(t *testing.T, account *service.Account, upstream service.HTTPUpstream) (string, error) {
	t.Helper()
	svc := service.NewAccountTestService(&requestProxyTransportRepo{account: account}, nil, nil, nil, nil, upstream, nil, nil)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/accounts/300/test", nil)
	err := svc.TestAccountConnection(c, account.ID, "gpt-6-astra", "", "")
	return recorder.Body.String(), err
}

func TestOpenAIRequestProxyLateAstraAdapterKeepsSelectedExit(t *testing.T) {
	account, _, outside, selectedHits, outsideHits := requestProxyTransportFixture(t)
	cfg := &config.Config{}
	cfg.Gateway.CodexGatewayPin = pinConfig()
	tickets := &requestProxyTransportTickets{proxy: outside}
	boundary, originHits := requestProxyNativeHTTPBoundary(t, NewHTTPUpstream(nil), nil)
	upstream := &astraRoutingUpstream{cfg: cfg, tickets: tickets, delegate: boundary}
	body, err := runRequestProxyTransportAccountTest(t, account, upstream)
	require.NoError(t, err)
	require.Contains(t, body, "test_complete")
	require.EqualValues(t, 1, selectedHits.Load())
	require.Zero(t, outsideHits.Load())
	require.Zero(t, originHits.Load())
	require.Zero(t, tickets.calls.Load(), "late adapter must not borrow a ticket or replace the selected route")
}

func TestOpenAIRequestProxyNativeTransportRejectsChangedExit(t *testing.T) {
	for _, changed := range []string{"outside", "direct"} {
		t.Run(changed, func(t *testing.T) {
			account, _, outside, selectedHits, outsideHits := requestProxyTransportFixture(t)
			upstream, originHits := requestProxyNativeHTTPBoundary(t, NewHTTPUpstream(nil), func(string) string {
				if changed == "direct" {
					return ""
				}
				return outside
			})
			body, err := runRequestProxyTransportAccountTest(t, account, upstream)
			require.Error(t, err)
			require.Contains(t, body, "selected OpenAI request proxies are unavailable")
			require.Zero(t, selectedHits.Load())
			require.Zero(t, outsideHits.Load())
			require.Zero(t, originHits.Load())
		})
	}
}
