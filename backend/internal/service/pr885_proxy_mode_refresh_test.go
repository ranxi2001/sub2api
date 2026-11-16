//go:build unit && bps_request_integration

package service

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type modeSwitchReader struct {
	*reauthTestAccountReader
	repo    *upstreamBillingProbeAccountRepo
	entered chan *openAIRequestProxyRoute
	resume  chan struct{}
	once    sync.Once
}

func (r *modeSwitchReader) GetAccount(ctx context.Context, id int64) (*Account, error) {
	if route, ok := openAIRequestProxyFromContext(ctx, id); ok {
		r.once.Do(func() {
			r.entered <- route
			select {
			case <-r.resume:
			case <-ctx.Done():
			}
		})
	}
	return r.repo.GetByID(ctx, id)
}

type modeSwitchTokenClient struct {
	*excelRefreshTestClient
	direct string
	calls  atomic.Int32
}

func (c *modeSwitchTokenClient) send(ctx context.Context, proxy string) error {
	c.calls.Add(1)
	tr := &http.Transport{}
	if proxy != "" {
		u, e := url.Parse(proxy)
		if e != nil {
			return e
		}
		tr.Proxy = http.ProxyURL(u)
	}
	defer tr.CloseIdleConnections()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.direct, strings.NewReader("grant_type=refresh_token"))
	if e != nil {
		return e
	}
	resp, e := (&http.Client{Transport: tr, Timeout: time.Second}).Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	_, e = io.Copy(io.Discard, resp.Body)
	return e
}
func (c *modeSwitchTokenClient) RefreshTokenWithClientID(ctx context.Context, rt, proxy, client string) (*openai.TokenResponse, error) {
	if err := c.send(ctx, proxy); err != nil {
		return nil, err
	}
	return c.excelRefreshTestClient.RefreshTokenWithClientID(ctx, rt, proxy, client)
}
func (c *modeSwitchTokenClient) SIWCToken(ctx context.Context, _ url.Values, proxy, _ string) (*openai.TokenResponse, *openai.SIWCIdentity, error) {
	if err := c.send(ctx, proxy); err != nil {
		return nil, nil, err
	}
	return &openai.TokenResponse{AccessToken: "refreshed-siwc", RefreshToken: "rotated", ExpiresIn: 3600}, nil, nil
}

func TestOpenAIRequestProxyModeSwitchRejectsRefreshBeforeSend(t *testing.T) {
	for _, kind := range []string{"forward", "images", "siwc"} {
		for _, single := range []string{"outside_proxy", "direct"} {
			t.Run(kind+"/"+single, func(t *testing.T) {
				var sends atomic.Int32
				gateway, account := localBPSRequestProxyFixture(t, 3, func(_ int, w http.ResponseWriter, r *http.Request) {
					sends.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, bpsRequestCompletedWire)
				})
				direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sends.Add(1); _, _ = io.WriteString(w, "{}") }))
				defer direct.Close()
				var reauth *OpenAIOAuthReauthService
				if kind == "siwc" {
					enableLocalSIWCRequestProxies(t, gateway, account)
					reauth = gateway.excelOAuthReauth
					store := reauth.repo.(*siwcTestRepo)
					var grant siwcGrant
					require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(store.ciphertext, "encrypted:")), &grant))
					grant.ExpiresAt = 0
					raw, err := json.Marshal(grant)
					require.NoError(t, err)
					store.ciphertext = "encrypted:" + string(raw)
				} else {
					svc, reader, repo, _, _ := newExcelReauthTestService(t)
					reauth = svc
					account.ID = reader.account.ID
					account.Credentials = cloneReauthMap(reader.account.Credentials)
					storeExcelTestCredentials(t, reauth, repo, excelTestCredentials("stale", time.Now().Add(-time.Hour)))
					gateway.excelOAuthReauth = reauth
					reauth.oauth.proxyRepo = gateway.proxyRepo
				}
				account.Extra[OpenAIProxyModeKey] = OpenAIProxyModeRequest
				account.Extra[OpenAIProxyIDsKey] = []int64{1, 2}
				account.Extra["openai_excel_bps_models"] = []any{"gpt-image-2", "gpt-6-astra"}
				oldProxy, err := gateway.proxyRepo.GetByID(t.Context(), 3)
				require.NoError(t, err)
				if single == "outside_proxy" {
					account.ProxyID = &oldProxy.ID
					account.Proxy = oldProxy
				} else {
					account.ProxyID = nil
					account.Proxy = nil
				}
				repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
				reader := &modeSwitchReader{reauthTestAccountReader: &reauthTestAccountReader{account: account, proxy: oldProxy}, repo: repo, entered: make(chan *openAIRequestProxyRoute, 1), resume: make(chan struct{})}
				reauth.accounts = reader
				client := &modeSwitchTokenClient{excelRefreshTestClient: &excelRefreshTestClient{credentials: excelTestCredentials("fresh", time.Now().Add(time.Hour))}, direct: direct.URL + "/oauth/token"}
				reauth.oauth.oauthClient = client
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					if kind == "images" {
						body := []byte(`{"model":"gpt-image-2","prompt":"fixture"}`)
						c, _ := newOpenAIImagesTestContext(t, body)
						parsed, e := gateway.ParseOpenAIImagesRequest(c, body)
						if e == nil {
							_, e = gateway.ForwardImages(ctx, c, account, body, parsed, "")
						}
						done <- e
						return
					}
					_, _, e := forwardLocalBPSRequest(ctx, gateway, account, []byte(`{"model":"gpt-6-astra","input":"fixture","stream":true}`))
					done <- e
				}()
				var route *openAIRequestProxyRoute
				select {
				case route = <-reader.entered:
				case e := <-done:
					t.Fatalf("request ended before scoped refresh: %v", e)
				case <-ctx.Done():
					t.Fatal("scoped refresh barrier not reached")
				}
				admin := &adminServiceImpl{accountRepo: repo, proxyRepo: gateway.proxyRepo}
				err = admin.UpdateAccountExtra(t.Context(), account.ID, map[string]any{OpenAIProxyModeKey: OpenAIProxyModeSingle})
				close(reader.resume)
				require.NoError(t, err)
				select {
				case err = <-done:
					require.Error(t, err)
				case <-ctx.Done():
					t.Fatal("request did not finish")
				}
				require.Zero(t, client.calls.Load(), "MODE_SWITCH_REFRESH_SENT: configuration change must reject before token/JWKS transport")
				require.Zero(t, sends.Load(), "neither the old single proxy nor direct endpoint may receive traffic")
				route.mu.Lock()
				finished := route.finished
				route.mu.Unlock()
				require.True(t, finished, "failed turn must release its route")
			})
		}
	}
}
