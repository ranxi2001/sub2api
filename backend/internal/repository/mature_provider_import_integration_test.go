//go:build integration

package repository_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type matureImportTransport func(*http.Request) (*http.Response, error)

func (f matureImportTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMatureProviderImportPreservesCanonicalData(t *testing.T) {
	db, client := repository.UpstreamChannelsTestDatabase(t)
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.Totp.EncryptionKey = strings.Repeat("78", 32)
	crypt, err := repository.NewAESEncryptor(cfg)
	require.NoError(t, err)
	channelService := service.NewChannelService(repository.NewChannelRepository(db), nil, nil, nil, nil)
	ch := admin.NewChannelHandler(channelService, nil, nil)
	ch.SetMatureProviderDependencies(crypt, &http.Client{Transport: matureImportTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "GET", r.Method, "import must never generate content or create an upstream key")
		data := `{"code":0,"data":{"4":0.25}}`
		if r.URL.Path == "/api/v1/keys" {
			data = `{"code":0,"data":{"items":[{"id":5,"name":"Example key","status":"active","key":"sk-synthetic-import-secret","group_id":4,"group":{"id":4,"name":"Example group","platform":"openai"}}],"total":1}}`
		} else {
			require.Equal(t, "/api/v1/groups/rates", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data))}, nil
	})})
	users := repository.NewUserRepository(client, db)
	svc := service.NewAdminService(cfg, users, repository.NewAdminGroupRepository(client, db), repository.NewAdminAccountRepository(client, db, nil), nil, nil, nil, nil, nil, nil, nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ah := admin.NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ah.SetMatureProviderCatalog(ch)
	r := gin.New()
	r.POST("/providers", ch.MutateMatureProvider)
	r.DELETE("/providers/:id", ch.MutateMatureProvider)
	r.POST("/accounts", ah.Create)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		require.Equal(t, 200, w.Code, w.Body.String())
		var out struct{ Data map[string]any }
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out.Data
	}
	_, err = db.Exec(`INSERT INTO channels(name,description) VALUES('canonical-price-channel','preserve'); INSERT INTO accounts(name,platform,type,credentials,extra) VALUES('existing-account','openai','apikey','{"api_key":"existing-synthetic","base_url":"https://existing.example.invalid"}','{"cost_multiplier":0.8}');`)
	require.NoError(t, err)
	snapshot := func() string {
		var raw string
		require.NoError(t, db.QueryRow(`SELECT jsonb_build_object('channels',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM channels c),'accounts',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM accounts a WHERE name='existing-account'),'balances',(SELECT jsonb_agg(jsonb_build_object('id',id,'balance',balance,'frozen',frozen_balance) ORDER BY id) FROM users))::text`).Scan(&raw))
		return raw
	}
	before := snapshot()
	p := decode(request("POST", "/providers", `{"name":"Fixture","site_type":"sub2api","base_url":"https://provider.example.invalid","auth_mode":"token","access_token":"synthetic-dashboard"}`))
	// The import must carry the revision read by the authenticated session, not 1.
	var revision int64
	require.NoError(t, db.QueryRow(`UPDATE mature_providers SET revision=revision+1 WHERE id=$1 RETURNING revision`, p["id"]).Scan(&revision))
	w := request("POST", "/accounts", fmt.Sprintf(`{"name":"Imported account","platform":"openai","type":"apikey","provider_id":%.0f,"provider_key_id":"5","credentials":{"openai_api_mode":"responses"},"extra":{"cost_multiplier":0.25}}`, p["id"]))
	created := decode(w)
	require.NotContains(t, w.Body.String(), "sk-synthetic-import-secret")
	var credentials, extra string
	require.NoError(t, db.QueryRow(`SELECT credentials::text,extra::text FROM accounts WHERE id=$1`, created["id"]).Scan(&credentials, &extra))
	require.Contains(t, credentials, "sk-synthetic-import-secret")
	require.Contains(t, credentials, "https://provider.example.invalid")
	var source struct {
		Binding struct {
			Revision int64 `json:"provider_revision"`
		} `json:"provider_binding"`
	}
	require.NoError(t, json.Unmarshal([]byte(extra), &source))
	require.Equal(t, revision, source.Binding.Revision)
	var importedKey string
	require.NoError(t, db.QueryRow(`SELECT key_id FROM provider_profit_account_at($1,clock_timestamp()) WHERE provider_id=$2`, created["id"], p["id"]).Scan(&importedKey))
	require.Equal(t, "5", importedKey)
	require.JSONEq(t, before, snapshot(), "provider import must preserve existing channels, accounts and balances")
	decode(request("DELETE", fmt.Sprintf("/providers/%.0f", p["id"]), fmt.Sprintf(`{"revision":%d,"confirm":true}`, revision)))
	var retained string
	require.NoError(t, db.QueryRow(`SELECT credentials::text FROM accounts WHERE id=$1`, created["id"]).Scan(&retained))
	require.Equal(t, credentials, retained)
	require.NoError(t, repository.ApplyMigrations(ctx, db), "migration replay must retain canonical and imported account data")
	require.JSONEq(t, before, snapshot())
}
