//go:build unit

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

// This oracle deliberately uses only APIs already present in the frozen old
// source. The same bytes run on old and candidate trees through real auth.
type issue1081AuthRoutingAccounts struct {
	service.AccountRepository
	account service.Account
}

func (r *issue1081AuthRoutingAccounts) ListSchedulableByPlatform(context.Context, string) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}
func (r *issue1081AuthRoutingAccounts) ListSchedulableByGroupIDAndPlatform(_ context.Context, id int64, _ string) ([]service.Account, error) {
	for _, group := range r.account.GroupIDs {
		if group == id {
			return []service.Account{r.account}, nil
		}
	}
	return nil, nil
}
func (r *issue1081AuthRoutingAccounts) ListModelAvailabilityCandidates(_ context.Context, group *int64, _ []string, include bool) ([]service.Account, error) {
	if group != nil {
		return r.ListSchedulableByGroupIDAndPlatform(context.Background(), *group, service.PlatformOpenAI)
	}
	if include {
		return []service.Account{r.account}, nil
	}
	return nil, nil
}

func TestIssue1081SimpleModeActualAuthSupportedModel(t *testing.T) {
	for _, google := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "google"}[google], func(t *testing.T) {
			id := int64(1)
			group := &service.Group{ID: id, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
			saved := &service.APIKey{ID: 11, UserID: 7, Key: "fixture-key", GroupID: &id, GroupIDs: []int64{1, 2}, Group: group, Status: service.StatusActive, User: &service.User{ID: 7, Role: service.RoleUser, Status: service.StatusActive, Concurrency: 5}}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			keys := service.NewAPIKeyService(&stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { k := *saved; return &k, nil }}, nil, nil, nil, nil, nil, cfg)
			keys.SetGroupPolicy(&issue1081LivePolicy{enabled: false})
			accounts := &issue1081AuthRoutingAccounts{account: service.Account{ID: 22, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{2}, AccountGroups: []service.AccountGroup{{GroupID: 2}}, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.1": "gpt-5.1"}}}}
			groups := issue1081QueueGroups{groups: map[int64]*service.Group{1: group}}
			gateway := service.NewGatewayService(accounts, groups, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			if google {
				router.Use(APIKeyAuthWithSubscriptionGoogle(keys, nil, cfg))
			} else {
				router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(keys, nil, cfg)))
			}
			reached := false
			router.POST("/v1/responses", func(c *gin.Context) {
				reached = true
				key, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				require.Equal(t, []int64{1}, key.ConfiguredGroupIDs())
				selected, err := gateway.SelectAccountForModelWithExclusions(c.Request.Context(), key.GroupID, "", "gpt-5.1", nil)
				require.ErrorIs(t, err, service.ErrNoAvailableAccounts, "ISSUE1081_AUTH_PRIMARY_POOL: nonempty supported extra B must not be selected")
				require.Nil(t, selected, "ISSUE1081_AUTH_PRIMARY_POOL: extra B selected after real authentication")
				probe, err := gateway.ProbeAPIKeyGroup(c.Request.Context(), key.Group, "gpt-5.1", "token")
				require.NoError(t, err)
				require.False(t, probe.Available, "ISSUE1081_AUTH_PRIMARY_PROBE")
				diagnosis := gateway.DiagnoseModelAvailabilityForPlatform(c.Request.Context(), key.GroupID, "gpt-5.1", service.PlatformOpenAI)
				require.True(t, diagnosis.Complete)
				require.False(t, diagnosis.HasModelSupport, "ISSUE1081_AUTH_PRIMARY_DIAGNOSIS")
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","input":"fixture"}`))
			req.Header.Set("Authorization", "Bearer fixture-key")
			req.Header.Set("x-goog-api-key", "fixture-key")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.True(t, reached, "real authentication must reach production routing: %s", rec.Body.String())
			require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			require.Equal(t, []int64{1, 2}, saved.GroupIDs, "shared auth source must retain extra bindings")
		})
	}
}

func TestIssue1081SimpleModeQueueConstraintIdentityChange(t *testing.T) {
	id := int64(1)
	saved := &service.APIKey{ID: 11, UserID: 7, GroupID: &id, GroupIDs: []int64{1, 2}, Group: &service.Group{ID: 1, Platform: service.PlatformOpenAI, Status: service.StatusActive}, Status: service.StatusActive, User: &service.User{ID: 7, Status: service.StatusActive}}
	policy := &issue1081LivePolicy{enabled: true}
	keys := service.NewAPIKeyService(issue1081QueueKeys{key: saved}, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple})
	keys.SetGroupPolicy(policy)
	initial, err := keys.GetByKeyUncached(context.Background(), "fixture-key")
	require.NoError(t, err)
	validate := newAPIKeyAuthRevalidator(keys.GetByKeyUncached, "fixture-key", "127.0.0.1", initial)
	_, err = validate(context.Background())
	require.NoError(t, err)
	policy.enabled = false
	_, err = validate(context.Background())
	require.Error(t, err, "queue/WS must re-enter auth when primary constraint changes even if selected primary is unchanged")
	require.Contains(t, err.Error(), "configuration changed")
}
