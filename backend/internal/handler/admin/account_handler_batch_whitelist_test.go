package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Only the storage and outbound HTTP boundaries are replaced. The same fixture
// drives Gin binding, credential selection, extraction, policy and apply for A/B.
type whitelistStore struct {
	service.AccountRepository
	accounts    map[int64]*service.Account
	extraWrites int
}

func (s *whitelistStore) UpdateExtra(context.Context, int64, map[string]any) error {
	s.extraWrites++
	return nil
}

func (s *whitelistStore) ClaimAutomaticUpstreamModelSync(context.Context, int64, time.Time, time.Time) (bool, error) {
	return false, fmt.Errorf("unexpected automatic claim")
}

func (s *whitelistStore) UpdateUpstreamModelMappingIfUnchanged(_ context.Context, account *service.Account, mapping map[string]string) (bool, error) {
	s.accounts[account.ID].Credentials["model_mapping"] = mapping
	return true, nil
}

type whitelistAdmin struct {
	*stubAdminService
	store *whitelistStore
}

func (s *whitelistAdmin) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	if account := s.store.accounts[id]; account != nil {
		return account, nil
	}
	return nil, service.ErrAccountNotFound
}

type whitelistTransport struct {
	syncUpstreamHTTPUpstream
	t       *testing.T
	lookups []int64
}

func (u *whitelistTransport) DoWithTLS(req *http.Request, proxy string, id int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	body, status := "{}", http.StatusOK
	if req.URL.Host != "models.dev" {
		require.Equal(u.t, http.MethodGet, req.Method)
		require.Equal(u.t, fmt.Sprintf("https://up-%d.example/v1/models", id), req.URL.String())
		require.Equal(u.t, fmt.Sprintf("Bearer fixture-%d", id), req.Header.Get("Authorization"))
		require.Equal(u.t, fmt.Sprintf("http://proxy-%d.example:80", id), proxy)
		u.lookups = append(u.lookups, id)
		switch id {
		case 43:
			status = http.StatusForbidden
		case 44:
			body = `{"data":[]}`
		case 45:
			status = http.StatusNotFound
		default:
			body = fmt.Sprintf(`{"data":[{"id":"model-%d","reasoning":true,"default_reasoning_level":"high","supported_reasoning_levels":["high"],"input_modalities":["text"],"context_window":8192},{"id":"model-%d"},{"id":"*"}]}`, id, id)
		}
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestAccountHandlerBatchUpstreamWhitelist(t *testing.T) {
	store := &whitelistStore{accounts: make(map[int64]*service.Account)}
	for id := int64(41); id <= 46; id++ {
		proxy := &service.Proxy{ID: id, Host: fmt.Sprintf("proxy-%d.example", id), Port: 80, Protocol: "http"}
		store.accounts[id] = &service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive,
			ProxyID: &proxy.ID, Proxy: proxy, Extra: map[string]any{"keep": true},
			Credentials: map[string]any{"base_url": fmt.Sprintf("https://up-%d.example/v1", id), "api_key": fmt.Sprintf("fixture-%d", id), "model_mapping": map[string]string{"old": "old", "public": fmt.Sprintf("model-%d", id)}}}
	}
	up := &whitelistTransport{t: t}
	svc := service.NewAccountTestService(store, nil, nil, nil, nil, up, &config.Config{}, nil)
	h := NewAccountHandler(&whitelistAdmin{newStubAdminService(), store}, nil, nil, nil, nil, nil, nil, nil, svc, nil, nil, nil, nil, nil)
	r := gin.New()
	r.POST("/accounts/:id/models/sync-upstream", h.SyncUpstreamModels)
	body := `{"apply":true,"whitelist_only":true}`
	post := func(id string, payload string, observer bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/accounts/"+id+"/models/sync-upstream", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if observer {
			req = req.WithContext(service.WithObserverScope(req.Context(), []int64{1}))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, id := range []int64{43, 41, 44, 42, 45} {
		w := post(fmt.Sprint(id), body, false)
		a := store.accounts[id]
		if id == 41 || id == 42 {
			require.Equal(t, 200, w.Code, w.Body.String())
			var result struct{ Data service.UpstreamModelCatalog }
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
			require.True(t, result.Data.Applied)
			model := fmt.Sprintf("model-%d", id)
			require.Equal(t, map[string]string{model: model, "public": model}, a.Credentials["model_mapping"])
		} else {
			if w.Code == 200 {
				require.Contains(t, w.Body.String(), `"skipped"`)
			} else {
				require.Equal(t, http.StatusBadGateway, w.Code)
			}
			require.Contains(t, a.Credentials["model_mapping"], "old")
		}
	}
	require.Zero(t, store.extraWrites, "whitelist-only apply must not persist catalog metadata or quality state")
	require.Contains(t, store.accounts[46].Credentials["model_mapping"], "old", "unselected account")
	require.Equal(t, 403, post("41", body, true).Code)
	for _, invalid := range []string{"0", "-1", "not-an-id"} {
		require.Equal(t, 400, post(invalid, body, false).Code)
	}
	require.Equal(t, 404, post("47", body, false).Code)
	require.Equal(t, 400, post("46", `{"whitelist_only":true}`, false).Code)
	require.Equal(t, 400, post("46", `{"whitelist_only":true,"apply":true,"automatic":true}`, false).Code)
	require.Equal(t, []int64{43, 41, 44, 42, 45}, up.lookups)
}

type whitelistActor struct {
	service.UserRepository
	user *service.User
}

func (r *whitelistActor) GetByID(context.Context, int64) (*service.User, error) { return r.user, nil }

func (r *whitelistActor) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

func TestAccountHandlerBatchWhitelistAuthentication(t *testing.T) {
	for _, tc := range []struct {
		role string
		code int
	}{{"", 401}, {service.RoleUser, 403}, {service.RoleObserver, 403}, {service.RoleAdmin, 404}} {
		t.Run(tc.role, func(t *testing.T) {
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "synthetic-whitelist", ExpireHour: 1}}
			auth := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
			actor := &service.User{ID: 19, Role: tc.role, Status: service.StatusActive}
			user := service.NewUserService(&whitelistActor{user: actor}, nil, nil, nil)
			h := NewAccountHandler(&whitelistAdmin{newStubAdminService(), &whitelistStore{accounts: map[int64]*service.Account{}}}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			r := gin.New()
			r.Use(gin.HandlerFunc(middleware.NewAdminAuthMiddleware(auth, user, nil, nil)))
			r.POST("/api/v1/admin/accounts/:id/models/sync-upstream", h.SyncUpstreamModels)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/41/models/sync-upstream", strings.NewReader(`{"apply":true,"whitelist_only":true}`))
			req.Header.Set("Content-Type", "application/json")
			if tc.role != "" {
				token, err := auth.GenerateToken(context.Background(), actor)
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tc.code, w.Code, w.Body.String())
		})
	}
}
