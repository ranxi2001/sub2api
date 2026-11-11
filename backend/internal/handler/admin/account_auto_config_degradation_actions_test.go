//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type degradationHandlerGroups struct {
	service.GroupRepository
	lookups []int64
}

func (g *degradationHandlerGroups) GetByID(_ context.Context, id int64) (*service.Group, error) {
	g.lookups = append(g.lookups, id)
	status := service.StatusActive
	if id == 6 {
		status = service.StatusDisabled
	}
	return &service.Group{ID: id, Status: status, Platform: service.PlatformOpenAI}, nil
}

func TestAutoConfigDegradationIndependentActionsRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := service.DefaultOAuthAutoConfig()
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &opsHandlerWritableSettings{raw: string(raw)}
	groups := &degradationHandlerGroups{}
	runtime := &config.Config{}
	runtime.Runtime.Role = config.RuntimeRoleGateway
	svc := service.ProvideAccountOpsService(repo, opsHandlerQueue{}, nil, nil, groups, runtime, nil, nil, nil, nil, nil)
	svc.Stop()
	handler := NewAccountOpsHandler(svc, nil)
	router := gin.New()
	router.PUT("/config", handler.SaveAutoConfig)
	router.GET("/config", handler.GetAutoConfig)

	save := func(body string, status int) service.OAuthAutoConfig {
		t.Helper()
		before := repo.raw
		out := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(out, req)
		require.Equal(t, status, out.Code, out.Body.String())
		if status != http.StatusOK {
			require.Equal(t, before, repo.raw, "rejected selection must not change settings")
		}
		var saved service.OAuthAutoConfig
		require.NoError(t, json.Unmarshal([]byte(repo.raw), &saved))
		return saved
	}

	// Disabled actions do not impose validation on their unused parameters.
	saved := save(`{"degradation_action":{"enabled":true,"threshold_percent":50,"limit_concurrency":false,"limit_models":false,"limit_groups":true,"group_ids":[5],"target_concurrency":0,"models":["gpt-*"]}}`, http.StatusOK)
	require.False(t, saved.DegradationAction.RestrictsConcurrency())
	require.False(t, saved.DegradationAction.RestrictsModels())
	require.True(t, saved.DegradationAction.LimitGroups)
	require.Equal(t, []int64{5}, saved.DegradationAction.GroupIDs)
	require.Equal(t, []int64{5}, groups.lookups)

	// An old editor updates nested legacy fields without resetting new choices.
	saved = save(`{"degradation_action":{"threshold_percent":60}}`, http.StatusOK)
	require.False(t, saved.DegradationAction.RestrictsConcurrency())
	require.False(t, saved.DegradationAction.RestrictsModels())
	require.True(t, saved.DegradationAction.LimitGroups)
	require.Equal(t, []int64{5}, saved.DegradationAction.GroupIDs)
	require.Equal(t, 60.0, *saved.DegradationAction.ThresholdPercent)
	save(`{"degradation_action":{"limit_groups":false}}`, http.StatusBadRequest)
	save(`{"degradation_action":{"group_ids":[]}}`, http.StatusBadRequest)
	save(`{"degradation_action":{"group_ids":[6]}}`, http.StatusBadRequest)
	save(`{"degradation_action":{"group_ids":[0]}}`, http.StatusBadRequest)
	save(`{"degradation_action":{"group_ids":[5,5]}}`, http.StatusBadRequest)
	save(`{"degradation_action":{"limit_concurrency":true}}`, http.StatusBadRequest)
	save(`{"degradation_action":{"limit_models":true}}`, http.StatusBadRequest)

	// An already saved group may later become unavailable; retain its scope.
	stored := saved
	stored.DegradationAction.GroupIDs = []int64{6}
	raw, err = json.Marshal(stored)
	require.NoError(t, err)
	repo.raw = string(raw)
	saved = save(`{"priority":7}`, http.StatusOK)
	require.Equal(t, []int64{6}, saved.DegradationAction.GroupIDs)
	require.Equal(t, cfg.ExcelBPS, saved.ExcelBPS)

	out := httptest.NewRecorder()
	router.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/config", nil))
	require.Equal(t, http.StatusOK, out.Code)
	var response struct {
		Data service.OAuthAutoConfig `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Body.Bytes(), &response))
	require.Equal(t, saved.DegradationAction, response.Data.DegradationAction)
	// New independent conditions survive both the HTTP round trip and old editors.
	saved = save(`{"degradation_action":{"probe_enabled":false,"threshold_percent":null,"upstream_429_enabled":true,"upstream_error_threshold_percent":85,"upstream_429_share_threshold_percent":90}}`, http.StatusOK)
	require.False(t, saved.DegradationAction.UsesProbe())
	require.True(t, saved.DegradationAction.UsesUpstream429())
	require.Nil(t, saved.DegradationAction.ThresholdPercent)
	require.Equal(t, 85.0, saved.DegradationAction.HTTPErrorThreshold())
	require.Equal(t, 90.0, saved.DegradationAction.HTTP429ShareThreshold())
	saved = save(`{"priority":8}`, http.StatusOK)
	require.False(t, saved.DegradationAction.UsesProbe())
	require.Equal(t, 90.0, saved.DegradationAction.HTTP429ShareThreshold())
	save(`{"degradation_action":{"upstream_429_enabled":false}}`, http.StatusBadRequest)
	save(`{"degradation_action":{"upstream_error_threshold_percent":0}}`, http.StatusBadRequest)
	saved = save(`{"degradation_action":{"probe_enabled":true,"threshold_percent":9,"upstream_429_enabled":false}}`, http.StatusOK)
	require.True(t, saved.DegradationAction.UsesProbe())
	require.False(t, saved.DegradationAction.UsesUpstream429())

}
