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

// The account repository owns saved configuration while the request keeps its
// hydrated snapshot. Only the SIWC refresh re-read pauses: ordinary admin reads
// must remain available so UpdateAccount can finish before refresh resumes.
type pr885SIWCConfigSwitchAccounts struct {
	AccountRepository
	mu      sync.Mutex
	account *Account
	proxies ProxyRepository
	ready   chan *openAIRequestProxyRoute
	resume  chan struct{}
	once    sync.Once
}

func pr885SIWCConfigSwitchClone(account *Account) *Account {
	copy := *account
	copy.Extra = cloneReauthMap(account.Extra)
	copy.Credentials = cloneReauthMap(account.Credentials)
	if account.ProxyID != nil {
		id := *account.ProxyID
		copy.ProxyID = &id
	}
	return &copy
}

func (r *pr885SIWCConfigSwitchAccounts) GetByID(context.Context, int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return pr885SIWCConfigSwitchClone(r.account), nil
}

func (r *pr885SIWCConfigSwitchAccounts) Update(_ context.Context, account *Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.account = pr885SIWCConfigSwitchClone(account)
	return nil
}

func (r *pr885SIWCConfigSwitchAccounts) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	return nil, nil
}

func (r *pr885SIWCConfigSwitchAccounts) GetAccount(ctx context.Context, id int64) (*Account, error) {
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
	return r.GetByID(ctx, id)
}

func (r *pr885SIWCConfigSwitchAccounts) GetProxy(ctx context.Context, id int64) (*Proxy, error) {
	return r.proxies.GetByID(ctx, id)
}

// Both an explicitly selected proxy and direct mode execute real loopback HTTP.
// The empty-proxy branch must send, rather than reject in a fake OAuth client,
// otherwise old production could appear safe without testing its direct leak.
type pr885SIWCConfigSwitchTokenClient struct {
	*reauthTestOAuthClient
	directURL string
}

func (c *pr885SIWCConfigSwitchTokenClient) SIWCToken(ctx context.Context, form url.Values, proxy, _ string) (*openai.TokenResponse, *openai.SIWCIdentity, error) {
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.Host == "" {
			return nil, nil, fmt.Errorf("invalid fixture proxy")
		}
		transport.Proxy = http.ProxyURL(u)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.directURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, nil, fmt.Errorf("controlled SIWC refresh status %d", resp.StatusCode)
	}
	var token openai.TokenResponse
	err = json.NewDecoder(resp.Body).Decode(&token)
	return &token, nil, err
}

func pr885SIWCConfigSwitchAssertLeaseReturned(t *testing.T, ctx context.Context, accountID int64) {
	t.Helper()
	// Hold the spare at load one, then make A the rotation cursor. A returned
	// request lease leaves A at zero, so it must win. A leaked lease ties at one
	// and rotates to the spare, making this a behavioral pool accounting check.
	spare, err := mihomo.AcquireBPSStaticRequestLease(ctx, accountID, []int64{2})
	require.NoError(t, err)
	defer spare.Release()
	cursor, err := mihomo.AcquireBPSStaticRequestLease(ctx, accountID, []int64{1})
	require.NoError(t, err)
	cursor.Release()
	next, err := mihomo.AcquireBPSStaticRequestLease(ctx, accountID, []int64{1, 2})
	require.NoError(t, err)
	defer next.Release()
	require.Equal(t, int64(1), next.ProxyID, "SIWC config-switch completion must return its acquired lease")
}

func pr885SIWCConfigSwitchRun(t *testing.T, singleProxyID int64, terminal string) {
	t.Helper()
	var observedMu sync.Mutex
	var observed []requestProxyObservation
	refreshReached := make(chan int, 1)
	refreshCanceled := make(chan struct{}, 1)
	handle := func(id int, w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		observedMu.Lock()
		observed = append(observed, requestProxyObservation{id: id, path: r.URL.Path, body: raw})
		observedMu.Unlock()
		if r.URL.Path == "/oauth/token" {
			if terminal != "" {
				refreshReached <- id
			}
			if terminal == "refresh_cancel" {
				<-r.Context().Done()
				refreshCanceled <- struct{}{}
				return
			}
			if terminal == "refresh_error" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"error":"controlled SIWC refresh failure"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"refreshed-siwc","refresh_token":"rotated-siwc","expires_in":3600}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, bpsRequestCompletedWire)
	}
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handle(0, w, r) }))
	t.Cleanup(direct.Close)
	gateway, account := localBPSRequestProxyFixture(t, 3, handle)
	enableLocalSIWCRequestProxies(t, gateway, account)
	account.Extra[OpenAIProxyIDsKey] = []int64{1}
	proxyA, err := gateway.proxyRepo.GetByID(t.Context(), 1)
	require.NoError(t, err)
	account.ProxyID, account.Proxy = &proxyA.ID, proxyA
	accounts := &pr885SIWCConfigSwitchAccounts{
		account: pr885SIWCConfigSwitchClone(account), proxies: gateway.proxyRepo,
		ready: make(chan *openAIRequestProxyRoute, 1), resume: make(chan struct{}),
	}
	reauth := gateway.excelOAuthReauth
	reauth.accounts = accounts
	store := reauth.repo.(*siwcTestRepo)
	var grant siwcGrant
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(store.ciphertext, "encrypted:")), &grant))
	grant.ExpiresAt = 0
	raw, err := json.Marshal(grant)
	require.NoError(t, err)
	store.ciphertext = "encrypted:" + string(raw)
	reauth.oauth.oauthClient = &pr885SIWCConfigSwitchTokenClient{directURL: direct.URL}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type result struct {
		code int
		body string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, body, err := forwardLocalBPSRequest(ctx, gateway, account, []byte(`{"model":"gpt-5.4","input":"test","stream":true}`))
		done <- result{code: code, body: body, err: err}
	}()
	var route *openAIRequestProxyRoute
	select {
	case route = <-accounts.ready:
	case <-ctx.Done():
		t.Fatal("SIWC Forward did not reach refresh account re-read after route acquisition")
	}
	require.Equal(t, int64(1), route.lease.ProxyID)
	t.Log("CONFIG_SWITCH_ROUTE_READY SIWC proxy_id=1")
	if terminal == "" {
		admin := &adminServiceImpl{accountRepo: accounts, proxyRepo: gateway.proxyRepo}
		extra := cloneReauthMap(account.Extra)
		extra[OpenAIProxyModeKey] = OpenAIProxyModeSingle
		saved, err := admin.UpdateAccount(ctx, account.ID, &UpdateAccountInput{Extra: extra, ProxyID: &singleProxyID})
		require.NoError(t, err)
		require.Equal(t, OpenAIProxyModeSingle, saved.Extra[OpenAIProxyModeKey])
		if singleProxyID == 0 {
			require.Nil(t, saved.ProxyID)
		} else {
			require.NotNil(t, saved.ProxyID)
			require.Equal(t, singleProxyID, *saved.ProxyID)
		}
		t.Logf("CONFIG_SWITCH_SINGLE_SAVED SIWC proxy_id=%d", singleProxyID)
	}
	close(accounts.resume)
	if terminal != "" {
		select {
		case id := <-refreshReached:
			require.Equal(t, 1, id, "refresh control must reach selected proxy A")
			t.Logf("SIWC_REFRESH_CONTROL_REACHED terminal=%s proxy_id=%d", terminal, id)
		case <-ctx.Done():
			t.Fatal("SIWC refresh control did not reach a real HTTP send")
		}
		if terminal == "refresh_cancel" {
			cancel()
			select {
			case <-refreshCanceled:
				t.Log("SIWC_REFRESH_CONTROL_CANCELED proxy_id=1")
			case <-time.After(5 * time.Second):
				t.Fatal("SIWC refresh HTTP request did not observe cancellation")
			}
		}
	}
	var completed result
	select {
	case completed = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SIWC Forward did not finish after configuration switch")
	}
	pr885SIWCConfigSwitchAssertLeaseReturned(t, t.Context(), account.ID)
	observedMu.Lock()
	hits := append([]requestProxyObservation(nil), observed...)
	observedMu.Unlock()
	if terminal != "" {
		require.Error(t, completed.err)
		if terminal == "refresh_error" {
			require.ErrorContains(t, completed.err, "controlled SIWC refresh status 502")
		}
		require.Len(t, hits, 1, "refresh error/cancellation must prevent inference and retry sends")
		require.Equal(t, 1, hits[0].id)
		require.Equal(t, "/oauth/token", hits[0].path)
		return
	}
	escaped := 0
	for _, hit := range hits {
		if hit.id != 1 {
			escaped++
		}
		t.Logf("SIWC config switch observed proxy_id=%d path=%s", hit.id, hit.path)
	}
	require.Zero(t, escaped, "CONFIG_SWITCH_REFRESH_EGRESS_ESCAPED: SIWC refresh/inference escaped request proxy A (B/direct sends must be zero)")
	require.Error(t, completed.err, "switching fresh configuration to single must explicitly reject the acquired request scope")
	require.Equal(t, "OPENAI_PROXY_UNAVAILABLE", infraerrors.Reason(completed.err))
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(completed.err))
	require.Empty(t, hits, "single configuration must reject before refresh or inference sends")
}

func TestExcelBPSRequestProxyConfigSwitchSIWC(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proxy int64
	}{{"single_B", 3}, {"single_empty", 0}} {
		t.Run(tc.name, func(t *testing.T) { pr885SIWCConfigSwitchRun(t, tc.proxy, "") })
	}
}

// These pass controls are intentionally outside the strict behavioral RED roots.
func TestExcelBPSRequestProxyConfigSwitchSIWCControls(t *testing.T) {
	for _, terminal := range []string{"refresh_error", "refresh_cancel"} {
		t.Run(terminal, func(t *testing.T) { pr885SIWCConfigSwitchRun(t, 3, terminal) })
	}
}
