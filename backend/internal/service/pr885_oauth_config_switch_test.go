//go:build unit && bps_request_integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

// This barrier belongs to the real OAuthRefreshAPI repository re-read. Admin
// UpdateAccount receives the original context, so it can save normally while
// the request-scoped GetByID waits with the already acquired inference lease.
type pr885OAuthConfigSwitchAccounts struct {
	AccountRepository
	mu      sync.Mutex
	account *Account
	ready   chan *openAIRequestProxyRoute
	resume  chan struct{}
	once    sync.Once
}

func pr885OAuthConfigSwitchClone(account *Account) *Account {
	copy := *account
	copy.Extra = cloneReauthMap(account.Extra)
	copy.Credentials = cloneReauthMap(account.Credentials)
	if account.ProxyID != nil {
		id := *account.ProxyID
		copy.ProxyID = &id
	}
	return &copy
}

func (r *pr885OAuthConfigSwitchAccounts) GetByID(ctx context.Context, id int64) (*Account, error) {
	if route, ok := openAIRequestProxyFromContext(ctx, id); ok {
		r.once.Do(func() {
			r.ready <- route
			select {
			case <-r.resume:
			case <-ctx.Done():
			}
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	return pr885OAuthConfigSwitchClone(r.account), nil
}

func (r *pr885OAuthConfigSwitchAccounts) Update(_ context.Context, account *Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.account = pr885OAuthConfigSwitchClone(account)
	return nil
}

func (r *pr885OAuthConfigSwitchAccounts) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	return nil, nil
}

type pr885OAuthConfigSwitchTokenClient struct {
	openaiOAuthClientRefreshStub
	directURL string
}

func (c *pr885OAuthConfigSwitchTokenClient) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxy, clientID string) (*openai.TokenResponse, error) {
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	endpoint := c.directURL + "/oauth/token"
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid fixture proxy")
		}
		transport.Proxy = http.ProxyURL(u)
		endpoint = "http://auth.openai.test/oauth/token"
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {clientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("controlled ordinary OAuth token endpoint status %d", resp.StatusCode)
	}
	var token openai.TokenResponse
	err = json.NewDecoder(resp.Body).Decode(&token)
	return &token, err
}

func pr885OAuthConfigSwitchAssertLeaseReturned(t *testing.T, accountID int64) {
	t.Helper()
	spare, err := mihomo.AcquireBPSStaticRequestLease(t.Context(), accountID, []int64{2})
	require.NoError(t, err)
	defer spare.Release()
	cursor, err := mihomo.AcquireBPSStaticRequestLease(t.Context(), accountID, []int64{1})
	require.NoError(t, err)
	cursor.Release()
	next, err := mihomo.AcquireBPSStaticRequestLease(t.Context(), accountID, []int64{1, 2})
	require.NoError(t, err)
	defer next.Release()
	require.Equal(t, int64(1), next.ProxyID, "ordinary OAuth refresh rejection must return request lease A")
}

type pr885OAuthConfigSwitchFixture struct {
	gateway  *OpenAIGatewayService
	account  *Account
	accounts *pr885OAuthConfigSwitchAccounts
	provider *OpenAITokenProvider
	hits     func() []requestProxyObservation
}

func newPR885OAuthConfigSwitchFixture(t *testing.T) pr885OAuthConfigSwitchFixture {
	t.Helper()
	var observedMu sync.Mutex
	var observed []requestProxyObservation
	handle := func(id int, w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		observedMu.Lock()
		observed = append(observed, requestProxyObservation{id: id, path: r.URL.Path, body: raw})
		observedMu.Unlock()
		if r.URL.Path == "/oauth/token" {
			// A real HTTP error also exercises the provider's production
			// default fallback policy. The fixture must not set Return to
			// conceal an inference send after a routing rejection.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":"controlled ordinary OAuth refresh failure"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, bpsRequestCompletedWire)
	}
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handle(0, w, r) }))
	t.Cleanup(direct.Close)
	gateway, account := localBPSRequestProxyFixture(t, 3, handle)
	delete(account.Extra, "openai_excel_bps")
	account.Extra[OpenAISIWCEnabledKey] = false
	account.Extra[OpenAIProxyModeKey] = OpenAIProxyModeRequest
	account.Extra[OpenAIProxyIDsKey] = []int64{1}
	account.Credentials["refresh_token"] = "ordinary-oauth-refresh"
	account.Credentials["expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	account.Credentials["client_id"] = "ordinary-oauth-client"
	proxyA, err := gateway.proxyRepo.GetByID(t.Context(), 1)
	require.NoError(t, err)
	account.ProxyID, account.Proxy = &proxyA.ID, proxyA
	accounts := &pr885OAuthConfigSwitchAccounts{
		account: pr885OAuthConfigSwitchClone(account),
		ready:   make(chan *openAIRequestProxyRoute, 1), resume: make(chan struct{}),
	}
	oauth := NewOpenAIOAuthService(gateway.proxyRepo, &pr885OAuthConfigSwitchTokenClient{directURL: direct.URL})
	t.Cleanup(oauth.Stop)
	provider := NewOpenAITokenProvider(accounts, nil, oauth)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(accounts, nil), NewOpenAITokenRefresher(oauth, accounts))
	gateway.openAITokenProvider = provider
	gateway.accountRepo = accounts
	require.Equal(t, ProviderRefreshErrorUseExistingToken, provider.refreshPolicy.OnRefreshError, "preserve the real default provider policy")
	return pr885OAuthConfigSwitchFixture{
		gateway: gateway, account: account, accounts: accounts, provider: provider,
		hits: func() []requestProxyObservation {
			observedMu.Lock()
			defer observedMu.Unlock()
			return append([]requestProxyObservation(nil), observed...)
		},
	}
}

func TestExcelBPSRequestProxyConfigSwitchOAuth(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proxy int64
	}{{"single_B", 3}, {"single_empty", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPR885OAuthConfigSwitchFixture(t)
			gateway, account, accounts, provider := f.gateway, f.account, f.accounts, f.provider
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, _, err := forwardLocalBPSRequest(ctx, gateway, account, []byte(`{"model":"gpt-5.4","input":"test","stream":true}`))
				done <- err
			}()
			var route *openAIRequestProxyRoute
			select {
			case route = <-accounts.ready:
			case <-ctx.Done():
				t.Fatal("ordinary OAuth Forward did not reach the real refresh API GetByID barrier")
			}
			require.Equal(t, int64(1), route.lease.ProxyID)
			require.Equal(t, int64(1), provider.SnapshotRuntimeMetrics().RefreshRequests)
			t.Log("CONFIG_SWITCH_ROUTE_READY OAuth proxy_id=1 OAuthRefreshAPI_GetByID=true")
			admin := &adminServiceImpl{accountRepo: accounts, proxyRepo: gateway.proxyRepo}
			extra := cloneReauthMap(account.Extra)
			extra[OpenAIProxyModeKey] = OpenAIProxyModeSingle
			saved, err := admin.UpdateAccount(ctx, account.ID, &UpdateAccountInput{Extra: extra, ProxyID: &tc.proxy})
			require.NoError(t, err)
			require.Equal(t, OpenAIProxyModeSingle, saved.Extra[OpenAIProxyModeKey])
			if tc.proxy == 0 {
				require.Nil(t, saved.ProxyID)
			} else {
				require.NotNil(t, saved.ProxyID)
				require.Equal(t, tc.proxy, *saved.ProxyID)
			}
			t.Logf("CONFIG_SWITCH_SINGLE_SAVED OAuth proxy_id=%d", tc.proxy)
			close(accounts.resume)
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatal("ordinary OAuth Forward did not finish after configuration switch")
			}
			pr885OAuthConfigSwitchAssertLeaseReturned(t, account.ID)
			hits := f.hits()
			escaped := 0
			for _, hit := range hits {
				if hit.id != 1 {
					escaped++
				}
				t.Logf("ordinary OAuth config switch observed proxy_id=%d path=%s", hit.id, hit.path)
			}
			require.Zero(t, escaped, "CONFIG_SWITCH_REFRESH_EGRESS_ESCAPED: ordinary OAuth refresh/inference escaped request proxy A (B/direct sends must be zero)")
			require.Error(t, err, "a fresh single configuration must reject the request before refresh and inference")
			require.Equal(t, "OPENAI_PROXY_UNAVAILABLE", infraerrors.Reason(err))
			require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
			require.Empty(t, hits, "request routing rejection must not fall back to old-token inference")
		})
	}
}

// Compatibility controls stay outside the strict config-switch RED root. An
// ordinary upstream refresh error must retain the default old-token fallback.
func TestExcelBPSRequestProxyConfigSwitchOAuthControls(t *testing.T) {
	t.Run("request_refresh_error", func(t *testing.T) {
		f := newPR885OAuthConfigSwitchFixture(t)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		type result struct {
			code int
			body string
			err  error
		}
		done := make(chan result, 1)
		go func() {
			code, body, err := forwardLocalBPSRequest(ctx, f.gateway, f.account, []byte(`{"model":"gpt-5.4","input":"test","stream":true}`))
			done <- result{code: code, body: body, err: err}
		}()
		select {
		case route := <-f.accounts.ready:
			require.Equal(t, int64(1), route.lease.ProxyID)
		case <-ctx.Done():
			t.Fatal("ordinary OAuth refresh control did not reach the real refresh API GetByID barrier")
		}
		close(f.accounts.resume)
		var completed result
		select {
		case completed = <-done:
		case <-ctx.Done():
			t.Fatal("ordinary OAuth refresh-error fallback did not finish")
		}
		require.NoError(t, completed.err)
		require.Equal(t, http.StatusOK, completed.code)
		require.Contains(t, completed.body, "response.completed")
		require.Equal(t, int64(1), f.provider.SnapshotRuntimeMetrics().RefreshRequests)
		pr885OAuthConfigSwitchAssertLeaseReturned(t, f.account.ID)
		hits := f.hits()
		require.Len(t, hits, 2, "refresh error must fall back to one inference using the old token")
		require.Equal(t, "/oauth/token", hits[0].path)
		require.NotEqual(t, "/oauth/token", hits[1].path)
		for _, hit := range hits {
			require.Equal(t, 1, hit.id, "refresh and old-token inference must remain on request proxy A")
		}
	})
	for _, tc := range []struct {
		name  string
		proxy int64
	}{
		{"single_B_no_route_refresh_error", 3},
		{"single_empty_no_route_refresh_error", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPR885OAuthConfigSwitchFixture(t)
			f.account.Extra[OpenAIProxyModeKey] = OpenAIProxyModeSingle
			f.account.ProxyID, f.account.Proxy = nil, nil
			if tc.proxy != 0 {
				proxy, err := f.gateway.proxyRepo.GetByID(t.Context(), tc.proxy)
				require.NoError(t, err)
				f.account.ProxyID, f.account.Proxy = &proxy.ID, proxy
			}
			require.NoError(t, f.accounts.Update(t.Context(), f.account))
			_, hasRoute := openAIRequestProxyFromContext(t.Context(), f.account.ID)
			require.False(t, hasRoute)
			token, err := f.provider.GetAccessToken(t.Context(), f.account)
			require.NoError(t, err)
			require.Equal(t, f.account.GetCredential("access_token"), token)
			require.NotEmpty(t, token)
			require.Equal(t, int64(1), f.provider.SnapshotRuntimeMetrics().RefreshRequests)
			hits := f.hits()
			require.Len(t, hits, 1, "single compatibility control must perform a real refresh HTTP request")
			require.Equal(t, int(tc.proxy), hits[0].id)
			require.Equal(t, "/oauth/token", hits[0].path)
		})
	}
}
