package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type oauthRequestProxyRepo struct {
	ProxyRepository
	proxies map[int64]*Proxy
}

func (r *oauthRequestProxyRepo) GetByID(_ context.Context, int64ID int64) (*Proxy, error) {
	if proxy := r.proxies[int64ID]; proxy != nil {
		return proxy, nil
	}
	return nil, errors.New("synthetic private proxy address")
}

type oauthRequestProxyClient struct {
	OpenAIOAuthClient
	proxies []string
}

func (c *oauthRequestProxyClient) ExchangeCode(_ context.Context, _, _, _, proxyURL, _ string) (*openai.TokenResponse, error) {
	c.proxies = append(c.proxies, proxyURL)
	return &openai.TokenResponse{AccessToken: "synthetic", ExpiresIn: 3600}, nil
}

func oauthRequestProxyFixture(t *testing.T, request bool) (*OpenAIOAuthService, *oauthRequestProxyRepo, *oauthRequestProxyClient, *OpenAIExchangeCodeInput) {
	t.Helper()
	repo := &oauthRequestProxyRepo{proxies: map[int64]*Proxy{
		11: {ID: 11, Protocol: "http", Host: "192.0.2.11", Port: 8080, Status: StatusActive},
		22: {ID: 22, Protocol: "http", Host: "192.0.2.22", Port: 8080, Status: StatusActive},
	}}
	client := &oauthRequestProxyClient{}
	svc := NewOpenAIOAuthService(repo, client)
	t.Cleanup(svc.Stop)
	svc.sessionStore.Set("synthetic-session", &openai.OAuthSession{State: "synthetic-state", CodeVerifier: "verifier", ProxyURL: repo.proxies[11].URL(), RequestProxy: request, CreatedAt: time.Now()})
	return svc, repo, client, &OpenAIExchangeCodeInput{SessionID: "synthetic-session", State: "synthetic-state", Code: "synthetic-code", ProxyMode: OpenAIProxyModeRequest, ProxyIDs: []int64{11, 22}}
}

func TestOpenAIOAuthRequestProxyExchangeKeepsOriginalExit(t *testing.T) {
	for _, clientID := range []string{openai.ClientID, openai.ExcelClientID} {
		t.Run(clientID, func(t *testing.T) {
			svc, repo, client, input := oauthRequestProxyFixture(t, true)
			session, _ := svc.sessionStore.Get(input.SessionID)
			session.ClientID = clientID
			input.ProxyIDs = []int64{22, 11}
			override := int64(22)
			input.ProxyID = &override
			_, err := svc.ExchangeCode(context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, []string{repo.proxies[11].URL()}, client.proxies, "group order or proxy_id must not replace the authorization exit")
		})
	}
}

func TestOpenAIOAuthRequestProxyExchangeRejectsInvalidOriginalExit(t *testing.T) {
	for _, change := range []string{"removed from group", "deleted", "expired", "inactive", "URL changed", "empty group", "invalid group", "mode single", "mode omitted"} {
		t.Run(change, func(t *testing.T) {
			svc, repo, client, input := oauthRequestProxyFixture(t, true)
			switch change {
			case "removed from group":
				input.ProxyIDs = []int64{22}
			case "deleted":
				delete(repo.proxies, 11)
			case "expired":
				expired := time.Now().Add(-time.Minute)
				repo.proxies[11].ExpiresAt = &expired
			case "inactive":
				repo.proxies[11].Status = StatusDisabled
			case "URL changed":
				repo.proxies[11].Host = "192.0.2.99"
			case "empty group":
				input.ProxyIDs = nil
			case "invalid group":
				input.ProxyIDs = []int64{11, -1}
			case "mode single":
				input.ProxyMode = OpenAIProxyModeSingle
			case "mode omitted":
				input.ProxyMode = ""
			}
			_, err := svc.ExchangeCode(context.Background(), input)
			require.Error(t, err)
			require.Empty(t, client.proxies)
			require.NotContains(t, err.Error(), "192.0.2")
			require.NotContains(t, err.Error(), "synthetic private proxy address")
			_, retained := svc.sessionStore.Get(input.SessionID)
			require.True(t, retained, "a rejected exchange must not consume the session")
		})
	}
}

func TestOpenAIOAuthRequestProxyExchangeCannotUpgradeSingleSession(t *testing.T) {
	svc, _, client, input := oauthRequestProxyFixture(t, false)
	_, err := svc.ExchangeCode(context.Background(), input)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Empty(t, client.proxies)
}

func TestOpenAIOAuthSingleProxyExchangeRetainsOverride(t *testing.T) {
	svc, repo, client, input := oauthRequestProxyFixture(t, false)
	input.ProxyMode = ""
	input.ProxyIDs = nil
	override := int64(22)
	input.ProxyID = &override
	_, err := svc.ExchangeCode(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, []string{repo.proxies[22].URL()}, client.proxies)
}
