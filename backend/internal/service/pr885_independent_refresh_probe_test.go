//go:build bps_request_integration

package service

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type independentExcelProxyClient struct{ *excelRefreshTestClient }

func (c *independentExcelProxyClient) RefreshTokenWithClientID(ctx context.Context, rt, proxy, clientID string) (*openai.TokenResponse, error) {
	u, err := url.Parse(proxy)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{Proxy: http.ProxyURL(u)}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://auth.openai.test/oauth/token", strings.NewReader("grant_type=refresh_token"))
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Transport: tr, Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return c.excelRefreshTestClient.RefreshTokenWithClientID(ctx, rt, proxy, clientID)
}

// Review-only behavioral oracle. No production source modifications.
func TestIndependent885BPSRefreshAndInferenceSameExit(t *testing.T) {
	observed := make(chan requestProxyObservation, 4)
	gateway, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		observed <- requestProxyObservation{id: id, path: r.URL.Path}
		if r.URL.Path == "/oauth/token" {
			io.WriteString(w, "{}")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, bpsRequestCompletedWire)
	})
	reauth, reader, repo, _, _ := newExcelReauthTestService(t)
	account.ID = reader.account.ID
	account.Credentials = reader.account.Credentials
	reader.account = account
	account.Extra[OpenAIProxyModeKey] = OpenAIProxyModeRequest
	account.Extra[OpenAIProxyIDsKey] = []int64{1, 2, 3}
	reauth.oauth.proxyRepo = gateway.proxyRepo
	gateway.excelOAuthReauth = reauth
	storeExcelTestCredentials(t, reauth, repo, excelTestCredentials("stale", time.Now().Add(-time.Hour)))
	reauth.oauth.oauthClient = &independentExcelProxyClient{&excelRefreshTestClient{credentials: excelTestCredentials("fresh", time.Now().Add(time.Hour))}}
	// Keep proxy 1 occupied by another turn. Inference must choose a less-busy
	// exit, while standalone maintenance currently still chooses proxy 1.
	lease, err := mihomo.AcquireBPSStaticRequestLease(t.Context(), account.ID, []int64{1})
	require.NoError(t, err)
	defer lease.Release()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	status, _, err := forwardLocalBPSRequest(ctx, gateway, account, []byte(`{"model":"gpt-6-astra","input":"local review fixture","stream":true}`))
	require.NoError(t, err)
	require.Equal(t, 200, status)
	var refresh, inference requestProxyObservation
	select {
	case refresh = <-observed:
	case <-ctx.Done():
		t.Fatal("missing token refresh")
	}
	select {
	case inference = <-observed:
	case <-ctx.Done():
		t.Fatal("missing inference")
	}
	require.Equal(t, "/oauth/token", refresh.path)
	require.Equal(t, "/basispoints/api/responses", inference.path)
	t.Logf("observed refresh_proxy=%d inference_proxy=%d", refresh.id, inference.id)
	require.Equal(t, refresh.id, inference.id, "BPS request-triggered refresh must share the inference exit")
}
