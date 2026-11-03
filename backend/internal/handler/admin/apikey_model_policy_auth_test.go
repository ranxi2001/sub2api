package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type modelPolicyAdminAuthUserRepo struct {
	service.UserRepository
	user *service.User
}

func (r *modelPolicyAdminAuthUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	return r.user, nil
}

func (r *modelPolicyAdminAuthUserRepo) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

func TestAdminAPIKeyModelPolicyRequiresAdminAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []string{"", service.RoleUser, service.RoleObserver, service.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "key-policy-auth-test-secret", ExpireHour: 1}}
			auth := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
			user := &service.User{ID: 1, Role: role, Status: service.StatusActive}
			users := service.NewUserService(&modelPolicyAdminAuthUserRepo{user: user}, nil, nil, nil)
			svc := newStubAdminService()
			svc.apiKeys = []service.APIKey{{ID: 10}}
			h := NewAdminAPIKeyHandler(svc)
			router := gin.New()
			admin := router.Group("/api/v1/admin", gin.HandlerFunc(middleware.NewAdminAuthMiddleware(auth, users, nil, nil)))
			admin.PUT("/api-keys/:id", h.UpdateGroup)
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(`{"model_mapping":{"gpt-6.1-sol":"gpt-5.6-sol"},"reasoning_effort":"high"}`))
			req.Header.Set("Content-Type", "application/json")
			if role != "" {
				token, err := auth.GenerateToken(context.Background(), user)
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			switch role {
			case "":
				require.Equal(t, http.StatusUnauthorized, rec.Code)
			case service.RoleAdmin:
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), `"reasoning_effort":"high"`)
			default:
				require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			}
		})
	}

	// Scoped administration keys gain only catalogued actions. Even granting
	// every current permission does not implicitly expose this full-admin route.
	all := adminaccess.Policy{Rules: []adminaccess.Rule{{AllGroups: true, IncludeUngrouped: true, AccountTypes: []string{"oauth", "apikey"}}}}
	for _, permission := range adminaccess.PermissionCatalog() {
		if permission.Scope == "global" {
			all.GlobalPermissions = append(all.GlobalPermissions, permission.ID)
		} else {
			all.Rules[0].Permissions = append(all.Rules[0].Permissions, permission.ID)
		}
	}
	require.False(t, adminaccess.RouteAllowed(all, http.MethodPut, "/api/v1/admin/api-keys/:id"))
}
