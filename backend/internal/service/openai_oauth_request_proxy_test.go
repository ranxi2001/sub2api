package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type openAIMaintenanceProxyRepo struct {
	ProxyRepository
	proxies   map[int64]*Proxy
	err       error
	requested []int64
}

func (r *openAIMaintenanceProxyRepo) GetByID(_ context.Context, id int64) (*Proxy, error) {
	r.requested = append(r.requested, id)
	if r.err != nil {
		return nil, r.err
	}
	return r.proxies[id], nil
}

type openAIMaintenanceOAuthClient struct {
	openaiOAuthClientRefreshStub
	proxies []string
}

func (s *openAIMaintenanceOAuthClient) RefreshTokenWithClientID(_ context.Context, _, proxyURL, _ string) (*openai.TokenResponse, error) {
	s.proxies = append(s.proxies, proxyURL)
	return nil, errors.New("fake token endpoint")
}

func TestOpenAIOAuthRefreshUsesSelectedRequestProxy(t *testing.T) {
	expired := time.Now().Add(-time.Hour)
	legacyID := int64(90)
	repo := &openAIMaintenanceProxyRepo{proxies: map[int64]*Proxy{
		1:  {ID: 1, Status: StatusActive, Protocol: "http", Host: "expired.example", Port: 8080, ExpiresAt: &expired},
		2:  {ID: 2, Status: StatusDisabled, Protocol: "http", Host: "disabled.example", Port: 8080},
		3:  {ID: 3, Status: StatusActive, Protocol: "http", Host: "selected.example", Port: 8080},
		90: {ID: 90, Status: StatusActive, Protocol: "http", Host: "legacy.example", Port: 8080},
	}}
	client := &openAIMaintenanceOAuthClient{}
	svc := NewOpenAIOAuthService(repo, client)
	defer svc.Stop()
	account := &Account{ID: 77, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &legacyID,
		Credentials: map[string]any{"refresh_token": "fake-refresh"},
		Extra:       map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []any{float64(1), float64(2), float64(3)}},
	}
	_, err := svc.RefreshAccountToken(context.Background(), account)
	require.ErrorContains(t, err, "fake token endpoint")
	require.Equal(t, []string{"http://selected.example:8080"}, client.proxies)
	require.Equal(t, []int64{1, 2, 3}, repo.requested)
	require.Equal(t, legacyID, *account.ProxyID)
}

func TestOpenAIOAuthRefreshRequestProxyUnavailableNeverCallsUpstream(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []any
		repo *openAIMaintenanceProxyRepo
	}{
		{name: "empty selection"},
		{name: "repository unavailable", ids: []any{float64(1)}},
		{name: "missing member", ids: []any{float64(1)}, repo: &openAIMaintenanceProxyRepo{}},
		{name: "lookup error", ids: []any{float64(1)}, repo: &openAIMaintenanceProxyRepo{err: errors.New("http://private:secret@example.invalid")}},
		{name: "unsupported proxy", ids: []any{float64(1)}, repo: &openAIMaintenanceProxyRepo{proxies: map[int64]*Proxy{1: {ID: 1, Status: StatusActive, Protocol: "file", Host: "bad.example", Port: 80}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, hasRefresh := range []bool{false, true} {
				client := &openAIMaintenanceOAuthClient{}
				var repository ProxyRepository
				if tc.repo != nil {
					repository = tc.repo
				}
				svc := NewOpenAIOAuthService(repository, client)
				defer svc.Stop()
				privacyCalls := 0
				svc.SetPrivacyClientFactory(func(string) (*req.Client, error) { privacyCalls++; return nil, errors.New("unexpected enrichment") })
				account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "fake-access"}, Extra: map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": tc.ids}}
				if hasRefresh {
					account.Credentials["refresh_token"] = "fake-refresh"
				}
				_, err := svc.RefreshAccountToken(context.Background(), account)
				require.ErrorContains(t, err, "direct fallback is disabled")
				require.NotContains(t, err.Error(), "secret")
				require.Empty(t, client.proxies)
				require.Zero(t, privacyCalls)
			}
		})
	}
}

func TestOpenAIOAuthRefreshExplicitSingleOverridesLegacyGroup(t *testing.T) {
	id := int64(90)
	repo := &openAIMaintenanceProxyRepo{proxies: map[int64]*Proxy{90: {ID: 90, Protocol: "http", Host: "single.example", Port: 8080}}}
	client := &openAIMaintenanceOAuthClient{}
	svc := NewOpenAIOAuthService(repo, client)
	defer svc.Stop()
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &id, Credentials: map[string]any{"refresh_token": "fake"}, Extra: map[string]any{"openai_proxy_mode": "single", "openai_excel_bps": true, "openai_excel_bps_proxy_mode": "request", "openai_excel_bps_proxy_ids": []any{float64(1)}}}
	_, err := svc.RefreshAccountToken(context.Background(), account)
	require.ErrorContains(t, err, "fake token endpoint")
	require.Equal(t, []string{"http://single.example:8080"}, client.proxies)
}

func TestOpenAIOAuthRefreshKeepsLogicalRequestExit(t *testing.T) {
	repo := &openAIMaintenanceProxyRepo{proxies: map[int64]*Proxy{
		1: {ID: 1, Status: StatusActive, Protocol: "http", Host: "first.example", Port: 8080},
		2: {ID: 2, Status: StatusActive, Protocol: "http", Host: "leased.example", Port: 8080},
	}}
	client := &openAIMaintenanceOAuthClient{}
	svc := NewOpenAIOAuthService(repo, client)
	defer svc.Stop()
	account := &Account{ID: 77, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "fake-refresh"}, Extra: map[string]any{"openai_proxy_mode": "request", "openai_proxy_ids": []any{float64(1), float64(2)}}}
	route := &openAIRequestProxyRoute{accountID: account.ID, proxy: "http://leased.example:8080"}
	ctx := context.WithValue(context.Background(), openAIRequestProxyContextKey{}, route)
	_, err := svc.RefreshAccountToken(ctx, account)
	require.ErrorContains(t, err, "fake token endpoint")
	require.Equal(t, []string{"http://leased.example:8080"}, client.proxies)
}
