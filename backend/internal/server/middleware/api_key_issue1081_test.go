package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type issue1081AdminUsers struct {
	service.UserRepository
	user *service.User
}

func (r issue1081AdminUsers) GetByID(context.Context, int64) (*service.User, error) {
	return r.user, nil
}
func TestIssue1081SettingRouteRequiresActiveAdmin(t *testing.T) {
	for _, tc := range []struct {
		role string
		want int
	}{{"", 401}, {service.RoleUser, 403}, {service.RoleObserver, 403}, {service.RoleAdmin, 204}} {
		t.Run(tc.role, func(t *testing.T) {
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "issue1081-fixture-secret", ExpireHour: 1}}
			auth := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
			actor := &service.User{ID: 7, Role: tc.role, Status: service.StatusActive}
			r := gin.New()
			r.Use(gin.HandlerFunc(NewAdminAuthMiddleware(auth, service.NewUserService(issue1081AdminUsers{user: actor}, nil, nil, nil), nil, nil)))
			writes := 0
			r.PUT("/api/v1/admin/settings", func(c *gin.Context) { writes++; c.Status(204) })
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", strings.NewReader(`{"api_key_multi_group_enabled":false}`))
			req.Header.Set("Content-Type", "application/json")
			if tc.role != "" {
				token, err := auth.GenerateToken(context.Background(), actor)
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tc.want, w.Code, w.Body.String())
			if tc.want != 204 {
				require.Zero(t, writes)
			}
		})
	}
}

func TestIssue1081QueueRevalidationRejectsDormantExtra(t *testing.T) {
	primary := int64(1)
	extra := int64(2)
	initial := &service.APIKey{ID: 11, UserID: 7, GroupID: &extra, GroupIDs: []int64{1, 2}, Status: service.StatusActive, User: &service.User{ID: 7, Status: service.StatusActive}, Group: &service.Group{ID: 2, Status: service.StatusActive}}
	latest := *initial
	latest.GroupID = &primary
	latest.GroupIDs = []int64{1}
	latest.Group = &service.Group{ID: 1, Status: service.StatusActive}
	revalidate := newAPIKeyAuthRevalidator(func(context.Context, string) (*service.APIKey, error) { return &latest, nil }, "fixture-key", "127.0.0.1", initial)
	_, err := revalidate(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "configuration changed")
	// Retained extras do not invalidate a main-group request when re-enabled.
	initial = &latest
	revalidate = newAPIKeyAuthRevalidator(func(context.Context, string) (*service.APIKey, error) {
		k := latest
		k.GroupIDs = []int64{1, 2}
		return &k, nil
	}, "fixture-key", "127.0.0.1", initial)
	_, err = revalidate(context.Background())
	require.NoError(t, err)
}
