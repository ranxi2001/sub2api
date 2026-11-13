package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type authorizationStatusClient struct {
	*reauthTestOAuthClient
	calls int
}

func (c *authorizationStatusClient) ConnectionToken(context.Context, url.Values, string, string, string) (*openai.TokenResponse, *openai.SIWCIdentity, error) {
	c.calls++
	return &openai.TokenResponse{AccessToken: "PRIVATE_ACCESS", RefreshToken: "PRIVATE_REFRESH", ExpiresIn: 3600,
			IDToken: reauthTestJWT(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "fixture-workspace", "chatgpt_user_id": "fixture-user"}})},
		&openai.SIWCIdentity{Subject: "fixture-subject"}, nil
}

func (*authorizationStatusClient) RevokeConnectionToken(context.Context, string, string, string) error {
	return nil
}

type authorizationStatusRepo struct {
	*connectionTypeRepo
	failSave bool
}

func (r *authorizationStatusRepo) CASAuthConnection(ctx context.Context, c *OpenAIAuthConnection, revision int64) (bool, error) {
	if r.failSave {
		return false, errors.New("fixture storage unavailable")
	}
	return r.connectionTypeRepo.CASAuthConnection(ctx, c, revision)
}

func TestConnectionAuthorizationStatusRequiresCommittedCallback(t *testing.T) {
	const id = "c910c45b-79a0-4367-a1dc-60e12210e199"
	for _, failSave := range []bool{false, true} {
		s, r := connectionTypeService(AccountTypeOAuth)
		base, _, _, _, oldClient, _ := newReauthTestService("fixture-workspace")
		defer base.oauth.Stop()
		s.oauth = base.oauth
		client := &authorizationStatusClient{reauthTestOAuthClient: oldClient}
		s.oauth.oauthClient = client
		r.rows = []*OpenAIAuthConnection{{ID: id, AccountID: 42, Kind: "bps", Status: "pending", Revision: 1}}
		s.repo = &authorizationStatusRepo{connectionTypeRepo: r, failSave: failSave}
		auth, err := s.BeginConnectionAuthorization(context.Background(), 42, id)
		require.NoError(t, err)
		parsed, err := url.Parse(auth.AuthURL)
		require.NoError(t, err)
		callback := openai.ExcelRedirectURI + "?code=fixture-code&state=" + url.QueryEscape(parsed.Query().Get("state"))
		err = s.CompleteConnectionAuthorization(context.Background(), 42, id, auth.SessionID, callback)
		if failSave {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
		status, err := s.ConnectionAuthorizationStatus(context.Background(), 42, id, auth.SessionID)
		require.NoError(t, err)
		if failSave {
			require.Equal(t, "unconfirmed", status.Status)
		} else {
			require.Equal(t, "saved", status.Status)
		}
		require.Error(t, s.CompleteConnectionAuthorization(context.Background(), 42, id, auth.SessionID, callback))
		require.Equal(t, 1, client.calls, "status query and duplicate callback must not exchange a code again")
	}
}

func TestConnectionAuthorizationStatusReceiptScopeAndExpiry(t *testing.T) {
	const sessionID = "1234567890abcdef1234567890abcdef"
	const id = "c910c45b-79a0-4367-a1dc-60e12210e199"
	for _, scenario := range []string{"saved", "different_session", "already_active", "expired", "disabled", "wrong_binding"} {
		t.Run(scenario, func(t *testing.T) {
			s, r := connectionTypeService(AccountTypeOAuth)
			// Use the existing in-memory session implementation, not an OAuth provider.
			base, _, _, _, _, _ := newReauthTestService("fixture-workspace")
			defer base.oauth.Stop()
			s.oauth = base.oauth
			c := &OpenAIAuthConnection{ID: id, AccountID: 42, Kind: "bps", Status: "active", ClientID: openai.ExcelClientID, Revision: 3}
			creds := map[string]any{"client_id": c.ClientID, "access_token": "PRIVATE_ACCESS", "refresh_token": "PRIVATE_REFRESH",
				connectionAuthorizationReceipt: connectionAuthorizationHash(sessionID), connectionAuthorizationTime: strconv.FormatInt(time.Now().Unix(), 10)}
			switch scenario {
			case "different_session":
				creds[connectionAuthorizationReceipt] = connectionAuthorizationHash(strings.Repeat("0", 32))
			case "already_active":
				delete(creds, connectionAuthorizationReceipt)
			case "expired":
				creds[connectionAuthorizationTime] = strconv.FormatInt(time.Now().Add(-openai.SessionTTL-time.Minute).Unix(), 10)
			case "disabled":
				c.Status = "disabled"
			}
			require.NoError(t, s.encryptConnectionCredentials(c, creds))
			if scenario == "wrong_binding" {
				c.AccountID = 43
			}
			r.rows = []*OpenAIAuthConnection{c}
			status, err := s.ConnectionAuthorizationStatus(context.Background(), 42, id, sessionID)
			if scenario == "wrong_binding" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			want := "unconfirmed"
			if scenario == "saved" {
				want = "saved"
			}
			require.Equal(t, want, status.Status)
			raw, err := json.Marshal(status)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "PRIVATE_")
			require.NotContains(t, string(raw), sessionID)
			require.Zero(t, r.writes)
		})
	}
}

func TestConnectionAuthorizationStatusWaitingDoesNotConsume(t *testing.T) {
	const id = "c910c45b-79a0-4367-a1dc-60e12210e199"
	const sessionID = "1234567890abcdef1234567890abcdef"
	s, r := connectionTypeService(AccountTypeOAuth)
	base, _, _, _, _, _ := newReauthTestService("fixture-workspace")
	defer base.oauth.Stop()
	s.oauth = base.oauth
	r.rows = []*OpenAIAuthConnection{{ID: id, AccountID: 42, Kind: "bps", Status: "pending", Revision: 1}}
	require.NoError(t, s.setSIWCSession(context.Background(), sessionID, &openai.OAuthSession{SIWCAccountID: 42, ConnectionID: id, ConnectionKind: "bps", ConnectionRevision: 1, CreatedAt: time.Now()}))
	for i := 0; i < 2; i++ {
		result, err := s.ConnectionAuthorizationStatus(context.Background(), 42, id, sessionID)
		require.NoError(t, err)
		require.Equal(t, "waiting", result.Status)
	}
	_, exists := s.readSIWCSession(context.Background(), sessionID, true)
	require.True(t, exists)
	result, err := s.ConnectionAuthorizationStatus(context.Background(), 42, id, sessionID)
	require.NoError(t, err)
	require.Equal(t, "unconfirmed", result.Status)
	_, err = s.ConnectionAuthorizationStatus(context.Background(), 42, id, "invalid")
	require.Error(t, err)
}
