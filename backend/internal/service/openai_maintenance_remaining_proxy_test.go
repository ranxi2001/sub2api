package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type remainingMaintenanceClient struct {
	*reauthTestOAuthClient
	credentials     map[string]any
	proxies         []string
	connectionCalls int
}

func (c *remainingMaintenanceClient) RefreshTokenWithClientID(_ context.Context, _, proxy, _ string) (*openai.TokenResponse, error) {
	c.proxies = append(c.proxies, proxy)
	return &openai.TokenResponse{AccessToken: reauthMapString(c.credentials, "access_token"), RefreshToken: reauthMapString(c.credentials, "refresh_token"), IDToken: reauthMapString(c.credentials, "id_token"), ExpiresIn: 3600}, nil
}

func (c *remainingMaintenanceClient) SIWCToken(_ context.Context, _ url.Values, proxy, _ string) (*openai.TokenResponse, *openai.SIWCIdentity, error) {
	c.proxies = append(c.proxies, proxy)
	identity := &openai.SIWCIdentity{Email: "user@example.com"}
	identity.Subject = "subscriber"
	return &openai.TokenResponse{AccessToken: "fresh-siwc-access", RefreshToken: "fresh-siwc-refresh", ExpiresIn: 3600, Scope: openai.SIWCDirectScope}, identity, nil
}

func (c *remainingMaintenanceClient) ConnectionToken(context.Context, url.Values, string, string, string) (*openai.TokenResponse, *openai.SIWCIdentity, error) {
	c.connectionCalls++
	identity := &openai.SIWCIdentity{}
	identity.Subject = "subscriber"
	return &openai.TokenResponse{AccessToken: "connection-access", RefreshToken: "connection-refresh", ExpiresIn: 3600}, identity, nil
}
func (c *remainingMaintenanceClient) RevokeConnectionToken(context.Context, string, string, string) error {
	c.connectionCalls++
	return nil
}

func remainingMaintenanceSelection(reader *reauthTestAccountReader, svc *OpenAIOAuthReauthService) *openAIMaintenanceProxyRepo {
	legacy := &Proxy{ID: 90, Protocol: "http", Host: "old.invalid", Port: 8080, Status: StatusActive}
	reader.proxy = legacy
	reader.account.ProxyID = &legacy.ID
	reader.account.Extra = map[string]any{OpenAIProxyModeKey: OpenAIProxyModeRequest, OpenAIProxyIDsKey: []int64{33}}
	repo := &openAIMaintenanceProxyRepo{proxies: map[int64]*Proxy{33: {ID: 33, Protocol: "http", Host: "selected.invalid", Port: 8080, Status: StatusActive}}}
	svc.oauth.proxyRepo = repo
	return repo
}

type remainingMaintenanceConnectionRepo struct {
	*connectionTypeRepo
}

func (r *remainingMaintenanceConnectionRepo) BeginAuthConnectionRefresh(context.Context, *OpenAIAuthConnection, string) (bool, error) {
	r.writes++
	return false, errors.New("unexpected refresh intent")
}

func TestRemainingMaintenanceExcelRefreshUsesRequestGroup(t *testing.T) {
	svc, reader, repo, _, _ := newExcelReauthTestService(t)
	defer svc.oauth.Stop()
	remainingMaintenanceSelection(reader, svc)
	storeExcelTestCredentials(t, svc, repo, excelTestCredentials("expired", time.Now().Add(-time.Hour)))
	client := &remainingMaintenanceClient{credentials: excelTestCredentials("fresh", time.Now().Add(time.Hour))}
	svc.oauth.oauthClient = client
	token, err := svc.ExcelAccessToken(context.Background(), reader.account, nil)
	require.NoError(t, err)
	require.Equal(t, client.credentials["access_token"], token)
	require.Equal(t, []string{"http://selected.invalid:8080"}, client.proxies)
	require.EqualValues(t, 90, *reader.account.ProxyID)
}

func TestRemainingMaintenanceSIWCUsesRequestGroup(t *testing.T) {
	svc, reader, base, _, oldClient, _ := newReauthTestService("acct-1")
	defer svc.oauth.Stop()
	store := &siwcTestRepo{reauthTestRepo: base}
	svc.repo = store
	remainingMaintenanceSelection(reader, svc)
	client := &remainingMaintenanceClient{reauthTestOAuthClient: oldClient}
	svc.oauth.oauthClient = client
	auth, err := svc.BeginSIWCAuthorization(context.Background(), reader.account.ID)
	require.NoError(t, err)
	session, ok := svc.readSIWCSession(context.Background(), auth.SessionID, false)
	require.True(t, ok)
	require.Equal(t, "http://selected.invalid:8080", session.ProxyURL)
	callback := openai.SIWCRedirectURI + "?code=test-code&state=" + url.QueryEscape(session.State) + "&client_id=issued"
	require.NoError(t, svc.CompleteSIWCAuthorization(context.Background(), reader.account.ID, auth.SessionID, callback, nil))
	require.Equal(t, []string{"http://selected.invalid:8080"}, client.proxies)
	plain, err := svc.encryptor.Decrypt(store.ciphertext)
	require.NoError(t, err)
	var grant siwcGrant
	require.NoError(t, json.Unmarshal([]byte(plain), &grant))
	grant.ExpiresAt = time.Now().Add(-time.Hour).Unix()
	raw, err := json.Marshal(grant)
	require.NoError(t, err)
	store.ciphertext, err = svc.encryptor.Encrypt(string(raw))
	require.NoError(t, err)
	_, err = svc.SIWCAccessToken(context.Background(), reader.account, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"http://selected.invalid:8080", "http://selected.invalid:8080"}, client.proxies)
}

func TestRemainingMaintenanceConnectionRequestModeRejectsBeforeSideEffects(t *testing.T) {
	for _, operation := range []string{"begin", "complete", "token", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			const id = "c910c45b-79a0-4367-a1dc-60e12210e199"
			svc, connections := connectionTypeService(AccountTypeOAuth)
			base, _, _, _, oldClient, _ := newReauthTestService("acct-1")
			defer base.oauth.Stop()
			svc.oauth = base.oauth
			reader, ok := svc.accounts.(*reauthTestAccountReader)
			require.True(t, ok)
			remainingMaintenanceSelection(reader, svc)
			client := &remainingMaintenanceClient{reauthTestOAuthClient: oldClient}
			svc.oauth.oauthClient = client
			c := &OpenAIAuthConnection{ID: id, AccountID: 42, Kind: "siwc", Status: "active", Revision: 1, ClientID: "issued", Subject: "subscriber", IdentityVerified: true, RefreshState: "idle", Capabilities: []string{"responses"}}
			require.NoError(t, svc.encryptConnectionCredentials(c, map[string]any{"client_id": "issued", "access_token": "old", "refresh_token": "old-refresh", "expires_at": "0"}))
			connections.rows = []*OpenAIAuthConnection{c}
			svc.repo = &remainingMaintenanceConnectionRepo{connectionTypeRepo: connections}
			session := &openai.OAuthSession{SIWCAccountID: 42, ConnectionID: id, ConnectionKind: c.Kind, ConnectionRevision: 1, State: "state", ClientID: "issued", RedirectURI: openai.SIWCRedirectURI, ProxyURL: "http://old.invalid:8080", CreatedAt: time.Now()}
			require.NoError(t, svc.setSIWCSession(context.Background(), "test-session", session))
			var err error
			switch operation {
			case "begin":
				_, err = svc.BeginConnectionAuthorization(context.Background(), 42, id)
			case "complete":
				err = svc.CompleteConnectionAuthorization(context.Background(), 42, id, "test-session", openai.SIWCRedirectURI+"?code=code&state=state&client_id=issued")
			case "token":
				_, err = svc.ConnectionAccessToken(context.Background(), 42, id)
			case "revoke":
				_, err = svc.RevokeAuthConnection(context.Background(), 42, id, 1)
			}
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "OPENAI_PROXY_CONNECTION_UNSUPPORTED", infraerrors.Reason(err))
			require.Zero(t, client.connectionCalls)
			require.Zero(t, connections.writes)
			require.EqualValues(t, 0, connections.rows[0].LeaseEpoch, "reject before taking a lifecycle lease")
			_, retained := svc.readSIWCSession(context.Background(), "test-session", false)
			require.True(t, retained, "reject before consuming the fixed-exit session")
		})
	}
}

func TestRemainingMaintenanceRequestRefreshUnavailableRetainsGrants(t *testing.T) {
	t.Run("excel", func(t *testing.T) {
		svc, reader, repo, _, _ := newExcelReauthTestService(t)
		defer svc.oauth.Stop()
		selection := remainingMaintenanceSelection(reader, svc)
		selection.err = errors.New("http://private:secret@private.invalid:8080")
		storeExcelTestCredentials(t, svc, repo, excelTestCredentials("expired", time.Now().Add(-time.Hour)))
		before := repo.ciphertext
		client := &remainingMaintenanceClient{credentials: excelTestCredentials("fresh", time.Now().Add(time.Hour))}
		svc.oauth.oauthClient = client
		_, err := svc.ExcelAccessToken(context.Background(), reader.account, nil)
		require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
		require.Equal(t, "OPENAI_PROXY_UNAVAILABLE", infraerrors.Reason(err))
		require.NotContains(t, err.Error(), "secret")
		require.Empty(t, client.proxies)
		require.Equal(t, before, repo.ciphertext)
	})
	t.Run("siwc", func(t *testing.T) {
		svc, reader, base, _, oldClient, _ := newReauthTestService("acct-1")
		defer svc.oauth.Stop()
		store := &siwcTestRepo{reauthTestRepo: base}
		svc.repo = store
		selection := remainingMaintenanceSelection(reader, svc)
		delete(selection.proxies, 33)
		raw, err := json.Marshal(siwcGrant{AccessToken: "expired", RefreshToken: "saved-refresh", ClientID: "issued", Subject: "subscriber", Email: "user@example.com", Workspace: "acct-1", Scope: openai.SIWCDirectScope, ExpiresAt: time.Now().Add(-time.Hour).Unix()})
		require.NoError(t, err)
		store.ciphertext, err = svc.encryptor.Encrypt(string(raw))
		require.NoError(t, err)
		before := store.ciphertext
		client := &remainingMaintenanceClient{reauthTestOAuthClient: oldClient}
		svc.oauth.oauthClient = client
		_, err = svc.SIWCAccessToken(context.Background(), reader.account, nil)
		require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
		require.Equal(t, "OPENAI_PROXY_UNAVAILABLE", infraerrors.Reason(err))
		require.Empty(t, client.proxies)
		require.Equal(t, before, store.ciphertext)
		require.Zero(t, store.writes)
	})
}

func TestRemainingMaintenanceSIWCCallbackRejectsStaleSessionExit(t *testing.T) {
	for _, beganSingle := range []bool{false, true} {
		t.Run(map[bool]string{false: "group changed", true: "single switched to request"}[beganSingle], func(t *testing.T) {
			svc, reader, base, _, oldClient, _ := newReauthTestService("acct-1")
			defer svc.oauth.Stop()
			store := &siwcTestRepo{reauthTestRepo: base}
			svc.repo = store
			selection := remainingMaintenanceSelection(reader, svc)
			if beganSingle {
				reader.account.Extra[OpenAIProxyModeKey] = OpenAIProxyModeSingle
			}
			client := &remainingMaintenanceClient{reauthTestOAuthClient: oldClient}
			svc.oauth.oauthClient = client
			auth, err := svc.BeginSIWCAuthorization(context.Background(), reader.account.ID)
			require.NoError(t, err)
			session, ok := svc.readSIWCSession(context.Background(), auth.SessionID, false)
			require.True(t, ok)
			reader.account.Extra[OpenAIProxyModeKey] = OpenAIProxyModeRequest
			reader.account.Extra[OpenAIProxyIDsKey] = []int64{44}
			selection.proxies[44] = &Proxy{ID: 44, Protocol: "http", Host: "replacement.invalid", Port: 8080, Status: StatusActive}
			callback := openai.SIWCRedirectURI + "?code=test-code&state=" + url.QueryEscape(session.State) + "&client_id=issued"
			err = svc.CompleteSIWCAuthorization(context.Background(), reader.account.ID, auth.SessionID, callback, nil)
			require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
			require.Equal(t, "OPENAI_PROXY_UNAVAILABLE", infraerrors.Reason(err))
			require.Empty(t, client.proxies)
			require.Zero(t, store.writes)
			_, retained := svc.readSIWCSession(context.Background(), auth.SessionID, false)
			require.True(t, retained, "reject stale egress before consuming OAuth code/session")
		})
	}
}
