package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type quotaMaintenanceProxyRepo struct {
	ProxyRepository
	proxies map[int64]*Proxy
	err     error
}

func (r *quotaMaintenanceProxyRepo) GetByID(_ context.Context, id int64) (*Proxy, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.proxies[id], nil
}

func quotaMaintenanceProxy(t *testing.T, id int64, endpoint string) *Proxy {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	require.NoError(t, err)
	host, portText, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	return &Proxy{ID: id, Protocol: parsed.Scheme, Host: host, Port: port, Status: StatusActive}
}

// Keep the actual transport behind local HTTP proxies. An unexpected direct or
// group-external URL fails before any transport can reach the network.
func quotaMaintenanceFactory(allowed ...string) PrivacyClientFactory {
	return func(proxyURL string) (*req.Client, error) {
		permitted := false
		for _, candidate := range allowed {
			permitted = permitted || proxyURL == candidate
		}
		if !permitted {
			return nil, fmt.Errorf("unexpected quota proxy")
		}
		client := req.C().SetProxyURL(proxyURL).SetTimeout(2 * time.Second)
		client.WrapRoundTripFunc(func(rt req.RoundTripper) req.RoundTripFunc {
			return func(r *req.Request) (*req.Response, error) {
				// Preserve the original hostname and path so the local server
				// receives an HTTP proxy request for the real quota endpoint.
				r.URL.Scheme = "http"
				return rt.RoundTrip(r)
			}
		})
		return client, nil
	}
}

func quotaMaintenanceResponse(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/backend-api/wham/usage":
		_, _ = w.Write([]byte(`{"plan_type":"plus","rate_limit_reset_credits":{"available_count":0}}`))
	case "/backend-api/wham/rate-limit-reset-credits":
		_, _ = w.Write([]byte(`{"available_count":0,"credits":[]}`))
	case "/backend-api/wham/rate-limit-reset-credits/consume":
		_, _ = w.Write([]byte(`{"code":"ok","windows_reset":1}`))
	default:
		http.NotFound(w, r)
	}
}

func TestOpenAIQuotaMaintenanceRequestProxyUsesSelectedGroup(t *testing.T) {
	var selectedHits, outsideHits atomic.Int32
	selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selectedHits.Add(1)
		require.True(t, r.URL.IsAbs(), "request must cross the actual local proxy boundary")
		require.Equal(t, "chatgpt.com", r.URL.Hostname())
		quotaMaintenanceResponse(w, r)
	}))
	defer selected.Close()
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outsideHits.Add(1)
		quotaMaintenanceResponse(w, r)
	}))
	defer outside.Close()

	legacy := quotaMaintenanceProxy(t, 99, outside.URL)
	disabled := quotaMaintenanceProxy(t, 11, outside.URL)
	disabled.Status = StatusDisabled
	expired := quotaMaintenanceProxy(t, 22, outside.URL)
	yesterday := time.Now().Add(-time.Hour)
	expired.ExpiresAt = &yesterday
	active := quotaMaintenanceProxy(t, 33, selected.URL)
	proxies := &quotaMaintenanceProxyRepo{proxies: map[int64]*Proxy{11: disabled, 22: expired, 33: active, 99: legacy}}
	account := &Account{
		ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"chatgpt_account_id": "quota-test-account"},
		ProxyID:     &legacy.ID, Proxy: legacy,
		Extra: map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []int64{11, 22, 33}},
	}
	accounts := &stubQuotaAccountRepo{accounts: map[int64]*Account{account.ID: account}}
	tokens := &stubQuotaTokenCache{tokens: map[string]string{OpenAITokenCacheKey(account): "fake-token"}}
	svc := NewOpenAIQuotaService(accounts, proxies, NewOpenAITokenProvider(accounts, tokens, nil), quotaMaintenanceFactory(selected.URL, outside.URL), nil)

	usage, err := svc.QueryUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "plus", usage.PlanType)
	require.EqualValues(t, 2, selectedHits.Load())
	require.Zero(t, outsideHits.Load(), "quota must not use the old group-external ProxyID")
	require.EqualValues(t, 99, *account.ProxyID, "maintenance must not mutate the saved single proxy")

	result, err := svc.ResetCredit(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "ok", result.Code)
	require.EqualValues(t, 3, selectedHits.Load())
	require.Zero(t, outsideHits.Load())

	_, err = svc.ResetCreditTargeted(context.Background(), account.ID, "test-credit", "test-redeem")
	require.NoError(t, err)
	require.EqualValues(t, 4, selectedHits.Load())
	require.Zero(t, outsideHits.Load())

	// Existing BPS request groups use the same maintenance boundary when the
	// newer OpenAI-wide fields have not yet been saved.
	account.Extra = map[string]any{
		"openai_excel_bps":   true,
		ExcelBPSProxyModeKey: ExcelBPSProxyModeRequest,
		ExcelBPSProxyIDsKey:  []int64{11, 22, 33},
	}
	_, err = svc.QueryUsage(context.Background(), account.ID)
	require.NoError(t, err)
	_, err = svc.ResetCredit(context.Background(), account.ID)
	require.NoError(t, err)
	require.EqualValues(t, 7, selectedHits.Load())
	require.Zero(t, outsideHits.Load())
	require.EqualValues(t, 99, *account.ProxyID)
}

func TestOpenAIQuotaMaintenanceRequestProxyUnavailableFailsBeforeToken(t *testing.T) {
	expired := time.Now().Add(-time.Hour)
	proxies := &quotaMaintenanceProxyRepo{proxies: map[int64]*Proxy{
		1: {ID: 1, Status: StatusDisabled, Protocol: "http", Host: "disabled.invalid", Port: 8080},
		2: {ID: 2, Status: StatusActive, Protocol: "http", Host: "expired.invalid", Port: 8080, ExpiresAt: &expired},
		3: {ID: 3, Status: StatusActive, Protocol: "http", Port: 8080},
		4: {ID: 4, Status: StatusActive, Protocol: "file", Host: "invalid.invalid", Port: 8080},
		5: {ID: 5, Status: StatusActive, Protocol: "http", Host: "invalid.invalid", Port: 70000},
	}}
	for _, tc := range []struct {
		name string
		mode any
		ids  any
		repo ProxyRepository
	}{
		{name: "empty group", mode: "request", repo: proxies},
		{name: "missing member", mode: "request", ids: []int64{99}, repo: proxies},
		{name: "inactive members", mode: "request", ids: []int64{1, 2}, repo: proxies},
		{name: "invalid members", mode: "request", ids: []int64{3, 4, 5}, repo: proxies},
		{name: "repository unavailable", mode: "request", ids: []int64{1}},
		{name: "lookup error", mode: "request", ids: []int64{1}, repo: &quotaMaintenanceProxyRepo{err: errors.New("http://private:secret@private.invalid:8080")}},
		{name: "invalid mode", mode: "round-robin", ids: []int64{1}, repo: proxies},
		{name: "invalid IDs", mode: "request", ids: []any{"1"}, repo: proxies},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy := &Proxy{ID: 90, Status: StatusActive, Protocol: "http", Host: "private.invalid", Port: 8080, Username: "private", Password: "secret"}
			account := &Account{
				ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &legacy.ID, Proxy: legacy,
				Credentials: map[string]any{"chatgpt_account_id": "quota-test-account"},
				Extra: map[string]any{"openai_proxy_mode": tc.mode, "openai_proxy_ids": tc.ids,
					"openai_excel_bps": true, ExcelBPSProxyModeKey: ExcelBPSProxyModeRequest, ExcelBPSProxyIDsKey: []int64{90}},
			}
			accounts := &stubQuotaAccountRepo{accounts: map[int64]*Account{account.ID: account}}
			factoryCalls := 0
			// A nil token provider would return NOT_CONFIGURED if validation
			// happened after token acquisition instead of failing closed first.
			svc := NewOpenAIQuotaService(accounts, tc.repo, nil, func(string) (*req.Client, error) {
				factoryCalls++
				return nil, errors.New("unexpected privacy client")
			}, nil)
			for _, operation := range []struct {
				name string
				call func() error
			}{
				{"query", func() error { _, err := svc.QueryUsage(context.Background(), account.ID); return err }},
				{"reset", func() error { _, err := svc.ResetCredit(context.Background(), account.ID); return err }},
				{"targeted reset", func() error {
					_, err := svc.ResetCreditTargeted(context.Background(), account.ID, "test-credit", "test-redeem")
					return err
				}},
			} {
				t.Run(operation.name, func(t *testing.T) {
					err := operation.call()
					require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
					require.Equal(t, "OPENAI_PROXY_UNAVAILABLE", infraerrors.Reason(err))
					require.NotContains(t, err.Error(), "secret")
					require.NotContains(t, err.Error(), "private.invalid")
					require.Zero(t, factoryCalls)
				})
			}
		})
	}
}

func TestOpenAIQuotaMaintenanceSinglePreservesExistingProxyResolution(t *testing.T) {
	for _, eager := range []bool{true, false} {
		t.Run(fmt.Sprintf("eager=%t", eager), func(t *testing.T) {
			var hits atomic.Int32
			single := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				quotaMaintenanceResponse(w, r)
			}))
			defer single.Close()
			proxy := quotaMaintenanceProxy(t, 99, single.URL)
			account := &Account{
				ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &proxy.ID,
				Credentials: map[string]any{"chatgpt_account_id": "quota-test-account"},
				Extra: map[string]any{"openai_proxy_mode": "single", "openai_proxy_ids": []int64{404},
					"openai_excel_bps": true, ExcelBPSProxyModeKey: ExcelBPSProxyModeRequest, ExcelBPSProxyIDsKey: []int64{404}},
			}
			var proxies ProxyRepository = &quotaMaintenanceProxyRepo{proxies: map[int64]*Proxy{proxy.ID: proxy}}
			if eager {
				account.Proxy = proxy
				proxies = nil
			}
			accounts := &stubQuotaAccountRepo{accounts: map[int64]*Account{account.ID: account}}
			tokens := &stubQuotaTokenCache{tokens: map[string]string{OpenAITokenCacheKey(account): "fake-token"}}
			svc := NewOpenAIQuotaService(accounts, proxies, NewOpenAITokenProvider(accounts, tokens, nil), quotaMaintenanceFactory(single.URL), nil)
			_, err := svc.QueryUsage(context.Background(), account.ID)
			require.NoError(t, err)
			_, err = svc.ResetCredit(context.Background(), account.ID)
			require.NoError(t, err)
			require.EqualValues(t, 3, hits.Load())
		})
	}
}

func TestOpenAIQuotaMaintenanceShadowUsesParentRequestProxy(t *testing.T) {
	var hits atomic.Int32
	selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		require.Equal(t, "parent-workspace", r.Header.Get("ChatGPT-Account-ID"))
		quotaMaintenanceResponse(w, r)
	}))
	defer selected.Close()
	proxy := quotaMaintenanceProxy(t, 33, selected.URL)
	parent := &Account{ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "parent-workspace"},
		Extra:       map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []int64{33}}}
	shadow := &Account{ID: 200, ParentAccountID: &parent.ID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, QuotaDimension: QuotaDimensionSpark}
	accounts := &stubQuotaAccountRepo{accounts: map[int64]*Account{parent.ID: parent, shadow.ID: shadow}}
	tokens := &stubQuotaTokenCache{tokens: map[string]string{OpenAITokenCacheKey(parent): "parent-token"}}
	proxies := &quotaMaintenanceProxyRepo{proxies: map[int64]*Proxy{33: proxy}}
	svc := NewOpenAIQuotaService(accounts, proxies, NewOpenAITokenProvider(accounts, tokens, nil), quotaMaintenanceFactory(selected.URL), nil)
	_, err := svc.QueryUsage(context.Background(), shadow.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, hits.Load())
	_, err = svc.ResetCredit(context.Background(), shadow.ID)
	require.ErrorIs(t, err, ErrSparkShadowResetNotSupported)
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))
	require.EqualValues(t, 2, hits.Load())
}
