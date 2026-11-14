package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Persisted fixtures bypass registration intentionally: new API key grants are
// forbidden, but existing grants must remain readable by the inference path.
type connectionTypeRepo struct {
	OpenAIOAuthReauthRepository
	OpenAIAuthConnectionRepository
	rows    []*OpenAIAuthConnection
	managed bool
	writes  int
}

func (r *connectionTypeRepo) ListAuthConnections(context.Context, int64) ([]*OpenAIAuthConnection, error) {
	result := make([]*OpenAIAuthConnection, 0, len(r.rows))
	for _, row := range r.rows {
		copy := *row
		result = append(result, &copy)
	}
	return result, nil
}
func (r *connectionTypeRepo) CreateAuthConnection(_ context.Context, c *OpenAIAuthConnection) error {
	r.writes++
	r.rows = append(r.rows, c)
	return nil
}
func (r *connectionTypeRepo) AuthConnectionsManaged(context.Context, int64) (bool, error) {
	return r.managed, nil
}
func (r *connectionTypeRepo) AcquireAuthConnectionLeaseProof(_ context.Context, _ int64, id, owner string) (*OpenAIAuthConnectionLease, error) {
	for _, c := range r.rows {
		if c.ID == id {
			c.LeaseOwner = owner
			c.LeaseEpoch++
			return &OpenAIAuthConnectionLease{Owner: owner, Epoch: c.LeaseEpoch}, nil
		}
	}
	return nil, nil
}
func (r *connectionTypeRepo) ReleaseAuthConnectionLease(context.Context, int64, string, string) error {
	return nil
}
func (r *connectionTypeRepo) CASAuthConnection(_ context.Context, c *OpenAIAuthConnection, revision int64) (bool, error) {
	for i, old := range r.rows {
		if old.ID == c.ID && old.Revision == revision {
			copy := *c
			copy.Revision++
			r.rows[i] = &copy
			r.writes++
			return true, nil
		}
	}
	return false, nil
}
func connectionTypeService(kind string) (*OpenAIOAuthReauthService, *connectionTypeRepo) {
	r := &connectionTypeRepo{}
	a := &reauthTestAccountReader{account: &Account{ID: 42, Platform: PlatformOpenAI, Type: kind, Credentials: map[string]any{"api_key": "synthetic-primary", "access_token": "synthetic-misleading-oauth"}}}
	return NewOpenAIOAuthReauthService(r, a, nil, nil, reauthTestEncryptor{}, true, nil, nil), r
}

func TestOpenAIConnectionManagementRejectsAPIKey(t *testing.T) {
	const id = "c910c45b-79a0-4367-a1dc-60e12210e199"
	operations := map[string]func(*OpenAIOAuthReauthService) error{
		"list": func(s *OpenAIOAuthReauthService) error {
			_, e := s.ListAuthConnections(context.Background(), 42)
			return e
		},
		"create": func(s *OpenAIOAuthReauthService) error {
			_, e := s.CreateAuthConnection(context.Background(), 42, CreateOpenAIAuthConnectionInput{Kind: "codex", Label: "Synthetic"})
			return e
		},
		"authorize": func(s *OpenAIOAuthReauthService) error {
			_, e := s.BeginConnectionAuthorization(context.Background(), 42, id)
			return e
		},
		"authorization_status": func(s *OpenAIOAuthReauthService) error {
			_, e := s.ConnectionAuthorizationStatus(context.Background(), 42, id, "1234567890abcdef1234567890abcdef")
			return e
		},
		"callback": func(s *OpenAIOAuthReauthService) error {
			return s.CompleteConnectionAuthorization(context.Background(), 42, id, "synthetic", "https://example.invalid")
		},
		"revoke": func(s *OpenAIOAuthReauthService) error {
			_, e := s.RevokeAuthConnection(context.Background(), 42, id, 1)
			return e
		},
		"rotate": func(s *OpenAIOAuthReauthService) error {
			_, e := s.RotateConnectionAPIKey(context.Background(), 42, id, 1, "synthetic")
			return e
		},
		"legacy": func(s *OpenAIOAuthReauthService) error { return s.SelectLegacyAuthConnection(context.Background(), 42) },
	}
	for _, action := range []string{"select", "enable", "disable", "unlink", "recover"} {
		operations[action] = func(s *OpenAIOAuthReauthService) error {
			_, e := s.UpdateAuthConnection(context.Background(), 42, id, UpdateOpenAIAuthConnectionInput{Action: action, Revision: 1})
			return e
		}
	}
	for name, op := range operations {
		t.Run(name, func(t *testing.T) {
			s, r := connectionTypeService(AccountTypeAPIKey)
			err := op(s)
			require.Error(t, err, "management must reject account.type=apikey even with OAuth-shaped credentials")
			require.Equal(t, "OPENAI_CONNECTION_ACCOUNT_INVALID", infraerrors.Reason(err))
			require.Zero(t, r.writes)
		})
	}
}

func TestOpenAIConnectionCreationRejectsMixedCredentials(t *testing.T) {
	for _, input := range []CreateOpenAIAuthConnectionInput{
		{Kind: "api_key", Label: "Mixed", APIKey: "synthetic"},
		{Kind: "codex", Label: "Mixed", APIKey: "synthetic"},
		{Kind: "bps", Label: "Mixed", APIKey: "synthetic"},
		{Kind: "siwc", Label: "Mixed", APIKey: "synthetic"},
	} {
		t.Run(input.Kind, func(t *testing.T) {
			s, r := connectionTypeService(AccountTypeOAuth)
			_, err := s.CreateAuthConnection(context.Background(), 42, input)
			require.Error(t, err, "OAuth registration must reject API key credentials")
			require.Zero(t, r.writes)
		})
	}
}
func TestOpenAIConnectionOAuthKindsRemainAvailable(t *testing.T) {
	for _, kind := range []string{"codex", "bps", "siwc"} {
		t.Run(kind, func(t *testing.T) {
			s, r := connectionTypeService(AccountTypeOAuth)
			c, err := s.CreateAuthConnection(context.Background(), 42, CreateOpenAIAuthConnectionInput{Kind: kind, Label: "Synthetic OAuth"})
			require.NoError(t, err)
			require.Equal(t, kind, c.Kind)
			require.Equal(t, "pending", c.Status)
			list, err := s.ListAuthConnections(context.Background(), 42)
			require.NoError(t, err)
			require.NotEmpty(t, list)
			require.Equal(t, 1, r.writes)
		})
	}
}
func TestOpenAIConnectionHistoricalAPIKeyRuntime(t *testing.T) {
	for _, parentType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(parentType, func(t *testing.T) {
			for _, status := range []int{401, 403} {
				t.Run(http.StatusText(status), func(t *testing.T) {
					s, r := connectionTypeService(parentType)
					r.managed = true
					c := &OpenAIAuthConnection{ID: "c910c45b-79a0-4367-a1dc-60e12210e199", AccountID: 42, Kind: "api_key", Status: "active", RefreshState: "idle", Revision: 1, Preferred: true, Capabilities: []string{"responses", "chat_completions", "embeddings"}}
					require.NoError(t, s.encryptConnectionCredentials(c, map[string]any{"api_key": "synthetic-persisted"}))
					r.rows = []*OpenAIAuthConnection{c}
					a, err := s.accounts.GetAccount(context.Background(), 42)
					require.NoError(t, err)
					gateway := &OpenAIGatewayService{excelOAuthReauth: s, cfg: &config.Config{}}
					for _, endpoint := range []string{"responses", "chat/completions", "embeddings"} {
						ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
						ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, strings.NewReader("{}"))
						projected, err := gateway.prepareAuthConnection(context.Background(), ctx, a, nil, "synthetic-model")
						require.NoError(t, err, "persisted managed API key route must remain usable")
						require.Equal(t, c.ID, projected.AuthConnectionID)
						require.Equal(t, AccountTypeAPIKey, projected.Type)
						require.Equal(t, "synthetic-persisted", projected.GetCredential("api_key"))
						require.Empty(t, projected.GetCredential("access_token"))
						token, err := s.ConnectionAccessToken(context.Background(), 42, c.ID)
						require.NoError(t, err)
						require.Equal(t, "synthetic-persisted", token)
						require.NoError(t, s.ValidateAuthConnectionSend(context.Background(), projected))
					}
					snapshot := &Account{ID: 42, AuthConnectionID: c.ID, AuthConnectionRevision: 1}
					require.NoError(t, s.FailAuthConnection(context.Background(), snapshot, status))
					require.Len(t, r.rows, 1)
					require.NotEmpty(t, r.rows[0].CredentialsCiphertext)
					if status == 401 {
						require.Equal(t, "pending", r.rows[0].Status)
					} else {
						require.NotNil(t, r.rows[0].CooldownUntil)
					}
					require.Error(t, s.ValidateAuthConnectionSend(context.Background(), snapshot))
				})
			}
		})
	}
}
func TestOpenAIConnectionOrdinaryAPIKeyRouteUnchanged(t *testing.T) {
	s, _ := connectionTypeService(AccountTypeAPIKey)
	gateway := &OpenAIGatewayService{excelOAuthReauth: s, cfg: &config.Config{}}
	a, err := s.accounts.GetAccount(context.Background(), 42)
	require.NoError(t, err)
	out, err := gateway.prepareAuthConnection(context.Background(), nil, a, nil, "synthetic-model")
	require.NoError(t, err)
	require.Same(t, a, out)
	require.Empty(t, out.AuthConnectionID)
	require.Equal(t, "synthetic-primary", out.GetCredential("api_key"))
	token, kind, err := gateway.GetAccessToken(context.Background(), out)
	require.NoError(t, err)
	require.Equal(t, "apikey", kind)
	require.Equal(t, "synthetic-primary", token)
}

func TestOpenAIConnectionManagementRejectsUnsupportedParents(t *testing.T) {
	for _, tc := range []struct {
		name, platform, kind string
		parentID             int64
	}{
		{"setup token", PlatformOpenAI, AccountTypeSetupToken, 0},
		{"unknown", PlatformOpenAI, "unknown", 0},
		{"other platform", PlatformAnthropic, AccountTypeOAuth, 0},
		{"shadow", PlatformOpenAI, AccountTypeOAuth, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r := connectionTypeService(tc.kind)
			reader, ok := s.accounts.(*reauthTestAccountReader)
			require.True(t, ok)
			a := reader.account
			a.Platform = tc.platform
			if tc.parentID != 0 {
				a.ParentAccountID = &tc.parentID
			}
			_, err := s.ListAuthConnections(context.Background(), 42)
			require.Error(t, err)
			require.Equal(t, "OPENAI_CONNECTION_ACCOUNT_INVALID", infraerrors.Reason(err))
			_, err = s.CreateAuthConnection(context.Background(), 42, CreateOpenAIAuthConnectionInput{Kind: "codex", Label: "Synthetic"})
			require.Error(t, err)
			require.Zero(t, r.writes)
		})
	}
}

func TestOpenAIConnectionManagementUnavailable(t *testing.T) {
	s := &OpenAIOAuthReauthService{}
	_, err := s.ListAuthConnections(context.Background(), 42)
	require.Equal(t, "OPENAI_CONNECTIONS_UNAVAILABLE", infraerrors.Reason(err))
	_, err = s.UpdateAuthConnection(context.Background(), 42, "c910c45b-79a0-4367-a1dc-60e12210e199", UpdateOpenAIAuthConnectionInput{Revision: 1, Action: "select"})
	require.Equal(t, "OPENAI_CONNECTIONS_UNAVAILABLE", infraerrors.Reason(err))
}
