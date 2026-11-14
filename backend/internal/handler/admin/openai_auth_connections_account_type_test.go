package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type connectionHandlerTypeRepo struct {
	service.OpenAIOAuthReauthRepository
	service.OpenAIAuthConnectionRepository
	writes int
}

func (r *connectionHandlerTypeRepo) ListAuthConnections(context.Context, int64) ([]*service.OpenAIAuthConnection, error) {
	return []*service.OpenAIAuthConnection{}, nil
}
func (r *connectionHandlerTypeRepo) CreateAuthConnection(context.Context, *service.OpenAIAuthConnection) error {
	r.writes++
	return nil
}

type connectionHandlerTypeAccounts struct{ kind string }

func (a connectionHandlerTypeAccounts) GetAccount(context.Context, int64) (*service.Account, error) {
	return &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: a.kind, Credentials: map[string]any{"access_token": "synthetic-misleading-oauth"}}, nil
}
func (connectionHandlerTypeAccounts) GetProxy(context.Context, int64) (*service.Proxy, error) {
	return nil, nil
}

type connectionHandlerTypeEncryptor struct{}

func (connectionHandlerTypeEncryptor) Encrypt(s string) (string, error) { return s, nil }
func (connectionHandlerTypeEncryptor) Decrypt(s string) (string, error) { return s, nil }

func TestOpenAIConnectionHandlerAccountType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const id = "c910c45b-79a0-4367-a1dc-60e12210e199"
	for _, kind := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		t.Run(kind, func(t *testing.T) {
			repo := &connectionHandlerTypeRepo{}
			svc := service.NewOpenAIOAuthReauthService(repo, connectionHandlerTypeAccounts{kind}, nil, nil, connectionHandlerTypeEncryptor{}, true, nil, nil)
			h := &OpenAIOAuthReauthHandler{service: svc}
			router := gin.New()
			prefix := "/accounts/:id/auth-connections"
			router.GET(prefix, h.ListAuthConnections)
			router.POST(prefix, h.CreateAuthConnection)
			router.POST(prefix+"/use-legacy", h.SelectLegacyAuthConnection)
			router.PATCH(prefix+"/:connection_id", h.UpdateAuthConnection)
			router.POST(prefix+"/:connection_id/authorize", h.BeginConnectionAuthorization)
			router.POST(prefix+"/:connection_id/callback", h.CompleteConnectionAuthorization)
			router.POST(prefix+"/:connection_id/authorization-status", h.ConnectionAuthorizationStatus)
			router.POST(prefix+"/:connection_id/revoke", h.RevokeAuthConnection)
			router.PUT(prefix+"/:connection_id/api-key", h.RotateConnectionAPIKey)
			cases := []struct{ name, method, suffix, body string }{
				{"list", "GET", "", ""},
				{"create", "POST", "", `{"kind":"codex","label":"Synthetic"}`},
			}
			if kind == service.AccountTypeAPIKey {
				cases = append(cases, []struct{ name, method, suffix, body string }{
					{"authorize", "POST", "/" + id + "/authorize", ""},
					{"callback", "POST", "/" + id + "/callback", `{"session_id":"synthetic","callback_url":"https://example.invalid"}`},
					{"authorization_status", "POST", "/" + id + "/authorization-status", `{"session_id":"1234567890abcdef1234567890abcdef"}`},
					{"revoke", "POST", "/" + id + "/revoke", `{"revision":1}`},
					{"rotate", "PUT", "/" + id + "/api-key", `{"revision":1,"api_key":"synthetic"}`},
					{"legacy", "POST", "/use-legacy", ""},
				}...)
				for _, action := range []string{"select", "disable", "enable", "unlink", "recover"} {
					cases = append(cases, struct{ name, method, suffix, body string }{action, "PATCH", "/" + id, `{"revision":1,"action":"` + action + `"}`})
				}
			} else {
				cases = append(cases, struct{ name, method, suffix, body string }{"reject_mixed", "POST", "", `{"kind":"api_key","label":"Mixed","api_key":"synthetic"}`})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					req := httptest.NewRequest(tc.method, "/accounts/42/auth-connections"+tc.suffix, strings.NewReader(tc.body))
					req.Header.Set("Content-Type", "application/json")
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, req)
					if kind == service.AccountTypeAPIKey {
						require.Equal(t, http.StatusBadRequest, rec.Code, "API key management must be refused: %s", rec.Body.String())
						require.Contains(t, rec.Body.String(), "OPENAI_CONNECTION_ACCOUNT_INVALID")
						require.Zero(t, repo.writes)
					} else if tc.name == "reject_mixed" {
						require.Equal(t, http.StatusBadRequest, rec.Code, "OAuth parent must reject API key creation: %s", rec.Body.String())
					} else {
						require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					}
				})
			}
		})
	}
}
