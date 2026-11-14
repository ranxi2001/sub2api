package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type moderationUpstreamAccounts struct {
	service.AccountRepository
	account *service.Account
}

func (r moderationUpstreamAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if r.account != nil && r.account.ID == id {
		return r.account, nil
	}
	return nil, service.ErrAccountNotFound
}

type moderationUpstreamUsers struct {
	service.UserRepository
	actor *service.User
}

func (r moderationUpstreamUsers) GetByID(context.Context, int64) (*service.User, error) {
	return r.actor, nil
}

// Real admin authentication -> JSON DTO -> service validation -> persisted
// setting -> a fresh service/handler GET. No browser adapter stands in for this.
func TestModerationUpstreamAdminSaveReloadAndAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := &moderationEngineSettings{value: `{"all_groups":true}`}
	account := &service.Account{ID: 51, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, GroupIDs: []int64{11, 12}}
	accounts := moderationUpstreamAccounts{account: account}
	auth := service.NewAuthService(nil, nil, nil, nil, &config.Config{JWT: config.JWTConfig{Secret: "fixture-moderation-secret", ExpireHour: 1}}, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, role := range []string{"", service.RoleUser, service.RoleObserver, service.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			actor := &service.User{ID: 7, Role: role, Status: service.StatusActive}
			h := NewContentModerationHandler(service.NewContentModerationService(settings, nil, nil, nil, nil, nil, nil, nil, accounts))
			router := gin.New()
			router.Use(gin.HandlerFunc(middleware.NewAdminAuthMiddleware(auth, service.NewUserService(moderationUpstreamUsers{actor: actor}, nil, nil, nil), nil, nil)))
			router.PUT("/api/v1/admin/risk-control/config", h.UpdateConfig)
			request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/risk-control/config", strings.NewReader(`{"upstream_account_scope":"selected","upstream_account_ids":[51]}`))
			request.Header.Set("Content-Type", "application/json")
			if role != "" {
				token, err := auth.GenerateToken(context.Background(), actor)
				require.NoError(t, err)
				request.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, request)
			if role == service.RoleAdmin {
				require.Equal(t, 200, w.Code, w.Body.String())
			} else {
				require.Contains(t, []int{401, 403}, w.Code)
				require.Equal(t, `{"all_groups":true}`, settings.value)
			}
		})
	}
	fresh := NewContentModerationHandler(service.NewContentModerationService(settings, nil, nil, nil, nil, nil, nil, nil, accounts))
	router := gin.New()
	router.GET("/config", fresh.GetConfig)
	router.PUT("/config", fresh.UpdateConfig)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/config", nil))
	var got struct {
		Data service.ContentModerationConfigView `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "selected", got.Data.UpstreamAccountScope)
	require.Equal(t, []int64{51}, got.Data.UpstreamAccountIDs)
	saved := settings.value
	for _, body := range []string{`{"upstream_account_scope":"unknown"}`, `{"upstream_account_ids":[0]}`, `{"upstream_account_ids":[999]}`, `{"upstream_account_ids":[51],"upstream_account_scope":"bad"}`} {
		req := httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, 400, w.Code, w.Body.String())
		require.Equal(t, saved, settings.value)
	}
	for _, kind := range []string{"unsupported-platform", "unsupported-type"} {
		oldPlatform, oldType := account.Platform, account.Type
		if kind == "unsupported-platform" {
			account.Platform = "customer"
		} else {
			account.Type = "bps"
		}
		req := httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(`{"upstream_account_ids":[51]}`))
		req.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, 400, w.Code)
		require.Equal(t, saved, settings.value)
		account.Platform, account.Type = oldPlatform, oldType
	}
	for _, body := range []string{`{"upstream_account_scope":"selected","upstream_account_ids":[]}`, `{"upstream_account_scope":"all","upstream_account_ids":[]}`} {
		req := httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, 200, w.Code, w.Body.String())
	}

}
