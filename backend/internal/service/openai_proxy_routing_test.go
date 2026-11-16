//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestOpenAIProxyRoutingUsesExplicitModeBeforeLegacy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		extra   map[string]any
		enabled bool
		bps     bool
		ids     []int64
	}{
		{"ordinary OAuth request", map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []any{float64(11), float64(22), float64(11)}}, true, false, []int64{11, 22}},
		{"single overrides legacy", map[string]any{"openai_excel_bps": true, "openai_proxy_mode": "single", "openai_proxy_ids": []int64{22}, ExcelBPSProxyModeKey: "request", ExcelBPSProxyIDsKey: []int64{11}}, false, false, []int64{22}},
		{"new group overrides legacy", map[string]any{"openai_excel_bps": true, "openai_proxy_mode": "request", "openai_proxy_ids": []int64{22}, ExcelBPSProxyModeKey: "request", ExcelBPSProxyIDsKey: []int64{11}}, true, true, []int64{22}},
		{"legacy BPS request", map[string]any{"openai_excel_bps": true, ExcelBPSProxyModeKey: "request", ExcelBPSProxyIDsKey: []int64{11}}, true, true, []int64{11}},
		{"disabled legacy BPS stays inactive", map[string]any{"openai_excel_bps": false, ExcelBPSProxyModeKey: "request", ExcelBPSProxyIDsKey: []int64{11}}, false, false, []int64{11}},
		{"empty group fails closed", map[string]any{"openai_proxy_mode": "request"}, true, false, nil},
		{"invalid group fails closed", map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []any{"11"}}, true, false, nil},
		{"invalid stored mode fails closed", map[string]any{"openai_proxy_mode": "invalid", "openai_proxy_ids": []int64{11}}, true, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: tc.extra}
			require.Equal(t, tc.enabled, account.OpenAIRequestProxyEnabled())
			require.Equal(t, tc.bps, account.ExcelBPSRequestProxyEnabled())
			require.Equal(t, tc.ids, account.OpenAIRequestProxyIDs())
			require.Equal(t, tc.ids, account.ExcelBPSRequestProxyIDs())
		})
	}
}

func TestOpenAIProxyRoutingRejectsUnsupportedAccounts(t *testing.T) {
	for _, credentials := range []map[string]any{{"auth_mode": "agentIdentity"}, {"auth_mode": "personalAccessToken"}, {"openai_auth_mode": "personal_access_token"}} {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: credentials, Extra: map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []int64{11}}}
		require.False(t, account.OpenAIRequestProxyEnabled())
	}
}

func TestAdminOpenAIProxyConfigurationRoundTrip(t *testing.T) {
	svc, _ := newBPSProxyConfigService()
	proxyID := int64(11)
	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name: "ordinary OAuth", Platform: PlatformOpenAI, Type: AccountTypeOAuth, SkipDefaultGroupBind: true, ProxyID: &proxyID, Concurrency: 6,
		Extra: map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []any{float64(11), float64(22), float64(11)}},
	})
	require.NoError(t, err)
	require.True(t, account.OpenAIRequestProxyEnabled())
	require.Equal(t, []int64{11, 22}, account.OpenAIRequestProxyIDs())
	require.Equal(t, []int64{11, 22}, account.Extra[OpenAIProxyIDsKey])

	account, err = svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{"unrelated": true}})
	require.NoError(t, err)
	require.True(t, account.OpenAIRequestProxyEnabled(), "older forms retain explicit settings")
	account, err = svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{"openai_proxy_mode": "single"}})
	require.NoError(t, err)
	require.False(t, account.OpenAIRequestProxyEnabled())
	require.Equal(t, []int64{11, 22}, account.OpenAIRequestProxyIDs())
	require.Equal(t, &proxyID, account.ProxyID)
	require.Equal(t, 6, account.Concurrency)
}

func TestAdminOpenAIProxyRejectsInvalidConfiguration(t *testing.T) {
	expired := time.Now().Add(-time.Minute)
	for _, tc := range []struct {
		name  string
		mode  any
		ids   any
		proxy *Proxy
	}{
		{"unknown mode", "round-robin", []int64{11}, nil},
		{"null mode", nil, []int64{11}, nil},
		{"empty group", "request", []int64{}, nil},
		{"missing proxy", "request", []int64{99}, nil},
		{"fractional ID", "request", []any{11.5}, nil},
		{"string ID", "request", []any{"11"}, nil},
		{"disabled proxy", "request", []int64{11}, &Proxy{ID: 11, Status: StatusDisabled}},
		{"expired proxy", "request", []int64{11}, &Proxy{ID: 11, Status: StatusActive, ExpiresAt: &expired}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newBPSProxyConfigService()
			if tc.proxy != nil {
				svc.proxyRepo = &bpsProxyConfigProxies{proxies: []Proxy{*tc.proxy}}
			}
			_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, SkipDefaultGroupBind: true, Extra: map[string]any{"openai_proxy_mode": tc.mode, "openai_proxy_ids": tc.ids}})
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
		})
	}
}

func TestAdminOpenAIProxyIDsRequireExplicitMode(t *testing.T) {
	svc, _ := newBPSProxyConfigService()
	_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, SkipDefaultGroupBind: true, Extra: map[string]any{OpenAIProxyIDsKey: []int64{11}}})
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
}

func TestAdminOpenAIProxyPartialAndBulkValidateMergedConfiguration(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		svc, repo := newBPSProxyConfigService()
		repo.account = &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []int64{11}}}
		var err error
		if bulk {
			_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Extra: map[string]any{"openai_proxy_ids": []any{}}})
		} else {
			err = svc.UpdateAccountExtra(context.Background(), 1, map[string]any{"openai_proxy_ids": []any{}})
		}
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
		require.Equal(t, []int64{11}, repo.account.OpenAIRequestProxyIDs())
	}
}

func TestAdminOpenAIProxyRejectsFixedAuthConnection(t *testing.T) {
	svc, repo := newBPSProxyConfigService()
	repo.account = &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, AuthConnectionID: "fixed-connection", Extra: map[string]any{}}
	_, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: map[string]any{OpenAIProxyModeKey: OpenAIProxyModeRequest, OpenAIProxyIDsKey: []int64{11}}})
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	_, err = svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: map[string]any{OpenAIProxyModeKey: OpenAIProxyModeSingle}})
	require.NoError(t, err)
}

func TestAdminOpenAIProxyRejectsManagedAuthConnections(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "legacy"}[legacy], func(t *testing.T) {
			svc, repo := newBPSProxyConfigService()
			repo.account = &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"openai_connections_managed": true}}
			extra := map[string]any{"openai_connections_managed": true, OpenAIProxyModeKey: OpenAIProxyModeRequest, OpenAIProxyIDsKey: []int64{11}}
			if legacy {
				extra = map[string]any{"openai_connections_managed": true, "openai_excel_bps": true, ExcelBPSProxyModeKey: ExcelBPSProxyModeRequest, ExcelBPSProxyIDsKey: []int64{11}}
			}
			_, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: extra})
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
		})
	}
}

func TestAdminOpenAIProxyCannotEraseManagedMarkerToEnableRequestMode(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "legacy"}[legacy], func(t *testing.T) {
			svc, repo := newBPSProxyConfigService()
			repo.account = &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"openai_connections_managed": true}}
			extra := map[string]any{OpenAIProxyModeKey: OpenAIProxyModeRequest, OpenAIProxyIDsKey: []int64{11}}
			if legacy {
				extra = map[string]any{"openai_excel_bps": true, ExcelBPSProxyModeKey: ExcelBPSProxyModeRequest, ExcelBPSProxyIDsKey: []int64{11}}
			}
			_, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: extra})
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "OPENAI_PROXY_CONNECTION_UNSUPPORTED", infraerrors.Reason(err))
		})
	}
}

type openAIProxyManagedPolicyRepo struct {
	OpenAIOAuthReauthRepository
	OpenAIAuthConnectionRepository
	managed bool
	err     error
}

func (r *openAIProxyManagedPolicyRepo) AuthConnectionsManaged(context.Context, int64) (bool, error) {
	return r.managed, r.err
}

func TestAdminOpenAIProxyChecksStoredManagedPolicy(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "legacy"}[legacy], func(t *testing.T) {
			svc, repo := newBPSProxyConfigService()
			repo.account = &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{}}
			svc.excelOAuthReauth = &OpenAIOAuthReauthService{repo: &openAIProxyManagedPolicyRepo{managed: true}}
			extra := map[string]any{OpenAIProxyModeKey: OpenAIProxyModeRequest, OpenAIProxyIDsKey: []int64{11}}
			if legacy {
				extra = map[string]any{"openai_excel_bps": true, ExcelBPSProxyModeKey: ExcelBPSProxyModeRequest, ExcelBPSProxyIDsKey: []int64{11}}
			}
			_, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: extra})
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "OPENAI_PROXY_CONNECTION_UNSUPPORTED", infraerrors.Reason(err))
		})
	}
}

func TestAdminOpenAIProxyManagedPolicyReadFailsClosed(t *testing.T) {
	svc, repo := newBPSProxyConfigService()
	repo.account = &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{}}
	svc.excelOAuthReauth = &OpenAIOAuthReauthService{repo: &openAIProxyManagedPolicyRepo{err: errors.New("sensitive database endpoint")}}
	_, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: map[string]any{OpenAIProxyModeKey: OpenAIProxyModeRequest, OpenAIProxyIDsKey: []int64{11}}})
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
	require.NotContains(t, err.Error(), "sensitive database endpoint")
}

func TestAdminOpenAIProxyMissingExitCanBeEditedOrDisabled(t *testing.T) {
	for _, input := range []*UpdateAccountInput{{Name: "renamed"}, {Extra: map[string]any{"unrelated": true}}, {Extra: map[string]any{OpenAIProxyModeKey: OpenAIProxyModeSingle}}} {
		svc, repo := newBPSProxyConfigService()
		repo.account = &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIProxyModeKey: OpenAIProxyModeRequest, OpenAIProxyIDsKey: []int64{99}}}
		updated, err := svc.UpdateAccount(context.Background(), 1, input)
		require.NoError(t, err)
		require.Equal(t, []int64{99}, updated.OpenAIRequestProxyIDs())
	}
}

func TestAdminOpenAIProxyBulkCredentialChangeCannotBypassEligibility(t *testing.T) {
	svc, repo := newBPSProxyConfigService()
	repo.account = &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIProxyModeKey: OpenAIProxyModeRequest, OpenAIProxyIDsKey: []int64{11}}}
	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Credentials: map[string]any{"auth_mode": "personalAccessToken"}})
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
}
