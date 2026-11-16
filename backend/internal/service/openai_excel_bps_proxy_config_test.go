//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAdminCreateBPSRequestProxyRejectsEmptyGroup(t *testing.T) {
	svc := &adminServiceImpl{accountRepo: &longContextBillingRepoStub{}}
	_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Platform: PlatformOpenAI, Type: AccountTypeOAuth, SkipDefaultGroupBind: true,
		Extra: map[string]any{"openai_excel_bps": true, "openai_excel_bps_proxy_mode": "request"},
	})
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
}

type bpsProxyConfigRepo struct {
	longContextBillingRepoStub
}

func (r *bpsProxyConfigRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	for key, value := range updates {
		r.account.Extra[key] = value
	}
	return nil
}

type bpsProxyConfigProxies struct {
	ProxyRepository
	proxies []Proxy
}

func (r *bpsProxyConfigProxies) ListByIDs(_ context.Context, ids []int64) ([]Proxy, error) {
	var proxies []Proxy
	for _, proxy := range r.proxies {
		for _, id := range ids {
			if proxy.ID == id {
				proxies = append(proxies, proxy)
				break
			}
		}
	}
	return proxies, nil
}

func newBPSProxyConfigService() (*adminServiceImpl, *bpsProxyConfigRepo) {
	repo := &bpsProxyConfigRepo{}
	return &adminServiceImpl{accountRepo: repo, proxyRepo: &bpsProxyConfigProxies{proxies: []Proxy{{ID: 11, Status: StatusActive}, {ID: 22, Status: StatusActive}}}}, repo
}

func TestAdminBPSRequestProxyConfigurationRoundTrip(t *testing.T) {
	svc, _ := newBPSProxyConfigService()
	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name: "bps account", Platform: PlatformOpenAI, Type: AccountTypeOAuth, SkipDefaultGroupBind: true, Concurrency: 6,
		Extra: map[string]any{"openai_excel_bps": true, ExcelBPSProxyModeKey: "request", ExcelBPSProxyIDsKey: []any{float64(11), float64(22), float64(11)}},
	})
	require.NoError(t, err)
	saved, err := svc.GetAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.True(t, saved.ExcelBPSRequestProxyEnabled())
	require.Equal(t, []int64{11, 22}, saved.ExcelBPSRequestProxyIDs())
	require.Equal(t, 6, saved.Concurrency)

	// An older form does not own the newly introduced keys.
	_, err = svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{"openai_excel_bps": true}})
	require.NoError(t, err)
	saved, err = svc.GetAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.True(t, saved.ExcelBPSRequestProxyEnabled())
	require.Equal(t, []int64{11, 22}, saved.ExcelBPSRequestProxyIDs())

	err = svc.UpdateAccountExtra(context.Background(), account.ID, map[string]any{ExcelBPSProxyIDsKey: []any{float64(22)}})
	require.NoError(t, err)
	saved, err = svc.GetAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{22}, saved.ExcelBPSRequestProxyIDs())
}

func TestAdminBPSRequestProxyRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        any
		ids         any
		platform    string
		accountType string
	}{
		{"unknown mode", "round-robin", []any{float64(11)}, PlatformOpenAI, AccountTypeOAuth},
		{"null mode", nil, []any{float64(11)}, PlatformOpenAI, AccountTypeOAuth},
		{"fractional ID", "request", []any{11.5}, PlatformOpenAI, AccountTypeOAuth},
		{"string ID", "request", []any{"11"}, PlatformOpenAI, AccountTypeOAuth},
		{"missing ID", "request", []any{float64(99)}, PlatformOpenAI, AccountTypeOAuth},
		{"negative ID", "request", []any{float64(-1)}, PlatformOpenAI, AccountTypeOAuth},
		{"non OAuth", "request", []any{float64(11)}, PlatformOpenAI, AccountTypeAPIKey},
		{"other platform", "request", []any{float64(11)}, PlatformGrok, AccountTypeOAuth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newBPSProxyConfigService()
			_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{Platform: tc.platform, Type: tc.accountType, SkipDefaultGroupBind: true, Extra: map[string]any{ExcelBPSProxyModeKey: tc.mode, ExcelBPSProxyIDsKey: tc.ids}})
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
		})
	}
}

func TestAdminBPSRequestProxyPartialAndBulkValidateMergedConfiguration(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		svc, repo := newBPSProxyConfigService()
		repo.account = &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{ExcelBPSProxyModeKey: "request", ExcelBPSProxyIDsKey: []int64{11}}}
		var err error
		if bulk {
			_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Extra: map[string]any{ExcelBPSProxyIDsKey: []any{}}})
		} else {
			err = svc.UpdateAccountExtra(context.Background(), 1, map[string]any{ExcelBPSProxyIDsKey: []any{}})
		}
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
		saved, err := svc.GetAccount(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, []int64{11}, saved.ExcelBPSRequestProxyIDs())
	}
}

func TestAdminBPSRequestProxyRejectsNewInactiveOrExpiredExit(t *testing.T) {
	expired := time.Now().Add(-time.Minute)
	for _, proxy := range []Proxy{{ID: 11, Status: StatusDisabled}, {ID: 11, Status: StatusActive, ExpiresAt: &expired}} {
		svc, _ := newBPSProxyConfigService()
		svc.proxyRepo = &bpsProxyConfigProxies{proxies: []Proxy{proxy}}
		_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, SkipDefaultGroupBind: true, Extra: map[string]any{"openai_excel_bps": true, ExcelBPSProxyModeKey: ExcelBPSProxyModeRequest, ExcelBPSProxyIDsKey: []int64{11}}})
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	}
}

func TestAdminBPSRequestProxyMissingExitCanBeEditedOrDisabled(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input *UpdateAccountInput
	}{
		{"unrelated edit", &UpdateAccountInput{Name: "renamed"}},
		{"older form", &UpdateAccountInput{Extra: map[string]any{"openai_excel_bps": true}}},
		{"disable BPS", &UpdateAccountInput{Extra: map[string]any{"openai_excel_bps": false}}},
		{"switch to session", &UpdateAccountInput{Extra: map[string]any{"openai_excel_bps": true, ExcelBPSProxyModeKey: ExcelBPSProxyModeSession}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newBPSProxyConfigService()
			repo.account = &Account{ID: 1, Name: "bps", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"openai_excel_bps": true, ExcelBPSProxyModeKey: ExcelBPSProxyModeRequest, ExcelBPSProxyIDsKey: []int64{99}}}
			_, err := svc.UpdateAccount(context.Background(), 1, tc.input)
			require.NoError(t, err)
			saved, err := svc.GetAccount(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, []int64{99}, saved.ExcelBPSRequestProxyIDs())
			if tc.name == "unrelated edit" || tc.name == "older form" {
				require.True(t, saved.ExcelBPSRequestProxyEnabled(), "stale groups must still fail closed at runtime")
			} else {
				require.False(t, saved.ExcelBPSRequestProxyEnabled())
			}
		})
	}
}

func TestAdminBPSRequestProxyCannotActivateMissingExit(t *testing.T) {
	svc, repo := newBPSProxyConfigService()
	repo.account = &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"openai_excel_bps": false, ExcelBPSProxyModeKey: ExcelBPSProxyModeRequest, ExcelBPSProxyIDsKey: []int64{99}}}
	_, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: map[string]any{"openai_excel_bps": true}})
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
}

func TestAdminBPSRequestProxyPartialAndBulkCannotActivateMissingExit(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		svc, repo := newBPSProxyConfigService()
		repo.account = &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"openai_excel_bps": false, ExcelBPSProxyModeKey: ExcelBPSProxyModeRequest, ExcelBPSProxyIDsKey: []int64{99}}}
		var err error
		if bulk {
			_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Extra: map[string]any{"openai_excel_bps": true}})
		} else {
			err = svc.UpdateAccountExtra(context.Background(), 1, map[string]any{"openai_excel_bps": true})
		}
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
		saved, err := svc.GetAccount(context.Background(), 1)
		require.NoError(t, err)
		require.False(t, saved.ExcelBPSRequestProxyEnabled())
	}
}
