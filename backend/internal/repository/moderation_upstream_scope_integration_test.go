//go:build integration

package repository_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestModerationUpstreamPostgresAdminRoundTrip(t *testing.T) {
	ctx := context.Background()
	client, moderationDB := repository.ModerationScopeTestClient(t)
	accounts := repository.NewAccountRepository(client, moderationDB, nil)
	groups := repository.NewGroupRepository(client, moderationDB)
	settings := repository.NewSettingRepository(client)
	old, oldErr := settings.GetValue(ctx, service.SettingKeyContentModerationConfig)
	t.Cleanup(func() {
		if oldErr == nil {
			_ = settings.Set(ctx, service.SettingKeyContentModerationConfig, old)
		} else {
			_ = settings.Delete(ctx, service.SettingKeyContentModerationConfig)
		}
	})
	require.NoError(t, settings.Set(ctx, service.SettingKeyContentModerationConfig, `{"all_groups":true}`))
	suffix := time.Now().UnixNano()
	account := &service.Account{Name: fmt.Sprintf("moderation-scope-%d", suffix), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{}, Extra: map[string]any{}}
	require.NoError(t, accounts.Create(ctx, account))
	t.Cleanup(func() {
		_, _ = moderationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, account.ID)
		_, _ = moderationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, account.ID)
	})
	group := &service.Group{Name: fmt.Sprintf("moderation-scope-group-%d", suffix), Platform: service.PlatformOpenAI, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard, RateMultiplier: 1}
	require.NoError(t, groups.Create(ctx, group))
	t.Cleanup(func() { _, _ = moderationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, group.ID) })
	user, err := client.User.Create().SetEmail(fmt.Sprintf("moderation-scope-%d@example.invalid", suffix)).SetPasswordHash("synthetic-not-a-login").SetRole(service.RoleAdmin).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = moderationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, user.ID) })
	users := repository.NewUserRepository(client, moderationDB)
	actor, err := users.GetByID(ctx, user.ID)
	require.NoError(t, err)
	auth := service.NewAuthService(nil, nil, nil, nil, &config.Config{JWT: config.JWTConfig{Secret: "synthetic-moderation-integration-secret", ExpireHour: 1}}, nil, nil, nil, nil, nil, nil, nil, nil)
	token, err := auth.GenerateToken(ctx, actor)
	require.NoError(t, err)
	newRouter := func() *gin.Engine {
		h := admin.NewContentModerationHandler(service.NewContentModerationService(settings, nil, nil, groups, nil, nil, nil, nil, accounts))
		r := gin.New()
		r.Use(gin.HandlerFunc(middleware.NewAdminAuthMiddleware(auth, service.NewUserService(users, nil, nil, nil), nil, nil)))
		r.PUT("/api/v1/admin/risk-control/config", h.UpdateConfig)
		r.GET("/api/v1/admin/risk-control/config", h.GetConfig)
		return r
	}
	request := func(router *gin.Engine, method, body, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/admin/risk-control/config", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	body := fmt.Sprintf(`{"upstream_account_scope":"selected","upstream_account_ids":[%d]}`, account.ID)
	w := request(newRouter(), http.MethodPut, body, "")
	require.Equal(t, 401, w.Code)
	w = request(newRouter(), http.MethodPut, body, token)
	require.Equal(t, 200, w.Code, w.Body.String())
	var stored service.ContentModerationConfig
	raw, err := settings.GetValue(ctx, service.SettingKeyContentModerationConfig)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	require.Equal(t, []int64{account.ID}, stored.UpstreamAccountIDs)
	require.Equal(t, service.PlatformOpenAI, stored.UpstreamAccountBindings[account.ID].Platform)
	w = request(newRouter(), http.MethodGet, "", token)
	require.Equal(t, 200, w.Code)
	var got struct {
		Data service.ContentModerationConfigView `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "selected", got.Data.UpstreamAccountScope)
	require.Equal(t, []int64{account.ID}, got.Data.UpstreamAccountIDs)
	// Search #ID, name, type/group and pagination use the real current-site query.
	matches, page, err := accounts.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 1}, "openai", "oauth", "", fmt.Sprintf("#%d", account.ID), service.AccountListGroupUngrouped, "")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	require.Equal(t, account.ID, matches[0].ID)
	require.EqualValues(t, 1, page.Total)
	require.NoError(t, accounts.BindGroups(ctx, account.ID, []int64{group.ID}))
	s := service.NewContentModerationService(settings, nil, nil, groups, nil, nil, nil, nil, accounts)
	current, err := accounts.GetByID(ctx, account.ID)
	require.NoError(t, err)
	_, err = s.UpstreamModerationApplies(ctx, &stored, service.ContentModerationCheckInput{UpstreamAccount: current})
	require.Error(t, err)
	w = request(newRouter(), http.MethodPut, body, token)
	require.Equal(t, 400, w.Code, w.Body.String())
	// Rebinding requires a successful removal save, then a deliberate re-add.
	w = request(newRouter(), http.MethodPut, `{"upstream_account_scope":"selected","upstream_account_ids":[]}`, token)
	require.Equal(t, 200, w.Code, w.Body.String())
	w = request(newRouter(), http.MethodPut, body, token)
	require.Equal(t, 200, w.Code, w.Body.String())
	raw, err = settings.GetValue(ctx, service.SettingKeyContentModerationConfig)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	require.Equal(t, []int64{group.ID}, stored.UpstreamAccountBindings[account.ID].GroupIDs)
	require.NoError(t, accounts.Delete(ctx, account.ID))
	w = request(newRouter(), http.MethodPut, body, token)
	require.Equal(t, 400, w.Code, w.Body.String())
	w = request(newRouter(), http.MethodPut, `{"upstream_account_scope":"selected","upstream_account_ids":[]}`, token)
	require.Equal(t, 200, w.Code, w.Body.String())
	w = request(newRouter(), http.MethodPut, `{"upstream_account_scope":"all"}`, token)
	require.Equal(t, 200, w.Code, w.Body.String())
}
