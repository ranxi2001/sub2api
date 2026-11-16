//go:build unit && bps_request_integration

package service

import (
	"context"
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

// The barrier is at the account repository boundary: Forward has already
// acquired its request route, while the grant reader has not read fresh state.
// Admin updates use GetByID and are therefore not blocked by this barrier.
type pr885ConfigSwitchAccounts struct {
	AccountRepository
	mu      sync.Mutex
	account *Account
	proxies ProxyRepository
	ready   chan context.Context
	resume  chan struct{}
	once    sync.Once
}

func pr885ConfigSwitchClone(a *Account) *Account {
	copy := *a
	copy.Extra = cloneReauthMap(a.Extra)
	copy.Credentials = cloneReauthMap(a.Credentials)
	return &copy
}

func (r *pr885ConfigSwitchAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	return pr885ConfigSwitchClone(r.account), nil
}
func (r *pr885ConfigSwitchAccounts) Update(_ context.Context, a *Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.account = pr885ConfigSwitchClone(a)
	return nil
}
func (r *pr885ConfigSwitchAccounts) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	return nil, nil
}
func (r *pr885ConfigSwitchAccounts) GetAccount(ctx context.Context, id int64) (*Account, error) {
	if _, ok := openAIRequestProxyFromContext(ctx, id); ok {
		r.once.Do(func() {
			r.ready <- ctx
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
func (r *pr885ConfigSwitchAccounts) GetProxy(ctx context.Context, id int64) (*Proxy, error) {
	return r.proxies.GetByID(ctx, id)
}

// Exercise a real HTTP refresh send at the external transport boundary. Even
// direct sends are redirected to a local server, so a forbidden empty proxy is
// observed without ever contacting or resolving an external OAuth endpoint.
type pr885ConfigSwitchOAuth struct {
	*excelRefreshTestClient
	directURL string
}

func (c *pr885ConfigSwitchOAuth) RefreshTokenWithClientID(ctx context.Context, rt, proxy, clientID string) (*openai.TokenResponse, error) {
	tr := &http.Transport{}
	defer tr.CloseIdleConnections()
	endpoint := c.directURL + "/oauth/token"
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return nil, err
		}
		tr.Proxy = http.ProxyURL(u)
		endpoint = "http://auth.openai.test/oauth/token"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("grant_type=refresh_token"))
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Transport: tr, Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("controlled refresh status %d", resp.StatusCode)
	}
	return c.excelRefreshTestClient.RefreshTokenWithClientID(ctx, rt, proxy, clientID)
}

// A held B and an explicitly reset A cursor make this behavioral pool check
// distinguish released A (load zero) from a leaked A (ties B and rotates to B).
func pr885ConfigSwitchRequireLeaseReturned(t *testing.T, accountID int64) {
	t.Helper()
	b, err := mihomo.AcquireBPSStaticRequestLease(t.Context(), accountID, []int64{2})
	require.NoError(t, err)
	defer b.Release()
	a, err := mihomo.AcquireBPSStaticRequestLease(t.Context(), accountID, []int64{1})
	require.NoError(t, err)
	a.Release()
	next, err := mihomo.AcquireBPSStaticRequestLease(t.Context(), accountID, []int64{1, 2})
	require.NoError(t, err)
	defer next.Release()
	require.Equal(t, int64(1), next.ProxyID, "completed/error/canceled request must return its A lease")
}

func pr885RunConfigSwitchRefresh(t *testing.T, images bool, singleProxy int64, control string) {
	t.Helper()
	var mu sync.Mutex
	var observed []requestProxyObservation
	handler := func(id int, w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		observed = append(observed, requestProxyObservation{id: id, path: r.URL.Path})
		mu.Unlock()
		if r.URL.Path == "/oauth/token" {
			if control == "refresh_error" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, "{}")
			return
		}
		if images {
			resp := openAIImagesJSONResponse()
			defer resp.Body.Close()
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			_, _ = io.Copy(w, resp.Body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, bpsRequestCompletedWire)
	}
	gateway, account := localBPSRequestProxyFixture(t, 2, handler)
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler(0, w, r) }))
	t.Cleanup(direct.Close)
	reauth, reader, repo, _, _ := newExcelReauthTestService(t)
	account.ID = reader.account.ID
	account.Credentials = reader.account.Credentials
	account.Extra[OpenAIProxyModeKey] = OpenAIProxyModeRequest
	account.Extra[OpenAIProxyIDsKey] = []int64{1}
	// Include both exercised models: restricting this to the image model makes
	// Forward deliberately select its ordinary Codex passthrough instead of BPS.
	account.Extra["openai_excel_bps_models"] = []any{"gpt-image-2", "gpt-6-astra"}
	mihomo.SetBPSStaticRequestGroups(map[int64][]int64{account.ID: {1, 2}})
	accounts := &pr885ConfigSwitchAccounts{account: pr885ConfigSwitchClone(account), proxies: gateway.proxyRepo, ready: make(chan context.Context, 1), resume: make(chan struct{})}
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(accounts.resume) }) }
	t.Cleanup(resume)
	reauth.accounts = accounts
	reauth.oauth.proxyRepo = gateway.proxyRepo
	gateway.excelOAuthReauth = reauth
	storeExcelTestCredentials(t, reauth, repo, excelTestCredentials("stale", time.Now().Add(-time.Hour)))
	reauth.oauth.oauthClient = &pr885ConfigSwitchOAuth{excelRefreshTestClient: &excelRefreshTestClient{credentials: excelTestCredentials("fresh", time.Now().Add(time.Hour))}, directURL: direct.URL}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	cancelRequest := cancel
	if images && control == "cancel" {
		// Images intentionally detaches ordinary client cancellation. Cancel the
		// production upstream owner so this control stops the actual route scope.
		ctx, cancelRequest = WithAPIKeyAdmissionOwner(ctx)
		defer cancelRequest()
	}
	type requestResult struct {
		status int
		body   string
		err    error
	}
	done := make(chan requestResult, 1)
	go func() {
		if !images {
			status, body, err := forwardLocalBPSRequest(ctx, gateway, account, []byte(`{"model":"gpt-6-astra","input":"config switch","stream":true}`))
			done <- requestResult{status: status, body: body, err: err}
			return
		}
		body := []byte(`{"model":"gpt-image-2","prompt":"config switch"}`)
		c, _ := newOpenAIImagesTestContext(t, body)
		parsed, err := gateway.ParseOpenAIImagesRequest(c, body)
		if err == nil {
			_, err = gateway.ForwardImages(ctx, c, account, body, parsed, "")
		}
		done <- requestResult{err: err}
	}()
	var barrierCtx context.Context
	select {
	case barrierCtx = <-accounts.ready:
	case result := <-done:
		t.Fatalf("request ended before account-reread barrier: %v", result.err)
	case <-ctx.Done():
		t.Fatal("missing account-reread barrier")
	}
	route, ok := openAIRequestProxyFromContext(barrierCtx, account.ID)
	require.True(t, ok)
	aProxy, err := gateway.proxyRepo.GetByID(t.Context(), 1)
	require.NoError(t, err)
	require.Equal(t, aProxy.URL(), route.proxy)
	require.Equal(t, account.ID, route.accountID)
	require.NotNil(t, route.lease)
	require.Equal(t, int64(1), route.lease.ProxyID)
	t.Logf("CONFIG_SWITCH_ROUTE_READY account=%d proxy=A", account.ID)
	if control == "" {
		extra := cloneReauthMap(account.Extra)
		extra[OpenAIProxyModeKey] = OpenAIProxyModeSingle
		admin := &adminServiceImpl{accountRepo: accounts, proxyRepo: gateway.proxyRepo}
		saved, err := admin.UpdateAccount(t.Context(), account.ID, &UpdateAccountInput{Extra: extra, ProxyID: &singleProxy})
		require.NoError(t, err)
		require.False(t, saved.OpenAIRequestProxyEnabled())
		if singleProxy == 0 {
			require.Nil(t, saved.ProxyID)
		} else {
			require.Equal(t, singleProxy, *saved.ProxyID)
		}
		t.Logf("CONFIG_SWITCH_SINGLE_SAVED account=%d proxy_id=%d", account.ID, singleProxy)
	} else {
		// Controls retain request A, so refresh_error must reach the actual HTTP
		// token endpoint rather than pass because config-switch rejection won.
		fresh, err := accounts.GetByID(t.Context(), account.ID)
		require.NoError(t, err)
		require.True(t, fresh.OpenAIRequestProxyEnabled())
		require.Equal(t, []int64{1}, fresh.OpenAIRequestProxyIDs())
	}
	if control == "cancel" {
		cancelRequest()
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		require.ErrorIs(t, barrierCtx.Err(), context.Canceled)
	}
	resume()
	var completed requestResult
	select {
	case completed = <-done:
	case <-ctx.Done():
		if control != "cancel" {
			t.Fatal("request did not finish after admin save")
		}
		select {
		case completed = <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("canceled request did not finish")
		}
	}
	mu.Lock()
	seen := append([]requestProxyObservation(nil), observed...)
	mu.Unlock()
	pr885ConfigSwitchRequireLeaseReturned(t, account.ID)
	for _, send := range seen {
		require.Equal(t, 1, send.id, "CONFIG_SWITCH_REFRESH_EGRESS_ESCAPED path=%s observed_proxy=%d; B/direct sends must be zero", send.path, send.id)
	}
	if control != "" {
		require.Error(t, completed.err)
		if control == "cancel" {
			require.Empty(t, seen, "cancellation at the reread barrier must prevent all sends")
		} else {
			require.Len(t, seen, 1, "refresh error control must make exactly one actual A refresh send")
			require.Equal(t, "/oauth/token", seen[0].path)
		}
		return
	}
	require.Error(t, completed.err, "fresh single configuration must explicitly reject the request route")
	require.Empty(t, seen, "config-switch rejection must precede every refresh/inference send")
	if images {
		require.Equal(t, "OPENAI_PROXY_UNAVAILABLE", infraerrors.Reason(completed.err))
		require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(completed.err))
	} else {
		// Forward intentionally translates credential failures into its public
		// BPS error; that boundary does not preserve the underlying error.
		var rejected *excelBPSForwardError
		require.ErrorAs(t, completed.err, &rejected)
		require.Equal(t, "basispoints_auth_unavailable", rejected.code)
		require.Equal(t, http.StatusBadGateway, completed.status)
		require.JSONEq(t, `{"error":{"type":"server_error","code":"basispoints_auth_unavailable","message":"Excel OAuth credential is unavailable; see Credential Operations"}}`, completed.body)
	}
}

func TestExcelBPSRequestProxyConfigSwitchForward(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proxy int64
	}{{"single_B", 2}, {"single_empty", 0}} {
		t.Run(tc.name, func(t *testing.T) { pr885RunConfigSwitchRefresh(t, false, tc.proxy, "") })
	}
}
func TestExcelBPSRequestProxyConfigSwitchImages(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proxy int64
	}{{"single_B", 2}, {"single_empty", 0}} {
		t.Run(tc.name, func(t *testing.T) { pr885RunConfigSwitchRefresh(t, true, tc.proxy, "") })
	}
}
func TestExcelBPSRequestProxyConfigSwitchControls(t *testing.T) {
	for _, images := range []bool{false, true} {
		for _, control := range []string{"refresh_error", "cancel"} {
			t.Run(fmt.Sprintf("images_%t/%s", images, control), func(t *testing.T) { pr885RunConfigSwitchRefresh(t, images, 2, control) })
		}
	}
}
