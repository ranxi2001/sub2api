//go:build unit

package admin

import (
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAutoConfigDegradationHTTPCompatibilityAndConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := service.DefaultOAuthAutoConfig()
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &opsHandlerWritableSettings{raw: string(raw)}
	runtime := &config.Config{}
	runtime.Runtime.Role = config.RuntimeRoleGateway
	svc := service.ProvideAccountOpsService(repo, opsHandlerQueue{}, nil, nil, modelBillingHandlerGroups{}, runtime, nil, nil, nil, nil, nil)
	svc.Stop()
	handler := NewAccountOpsHandler(svc, nil)
	router := gin.New()
	router.PUT("/config", handler.SaveAutoConfig)
	router.GET("/config", handler.GetAutoConfig)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"threshold required", `{"degradation_action":{"enabled":true}}`, 400},
		{"negative threshold", `{"degradation_action":{"threshold_percent":-1}}`, 400},
		{"over threshold", `{"degradation_action":{"threshold_percent":101}}`, 400},
		{"fractional concurrency", `{"degradation_action":{"target_concurrency":1.5}}`, 400},
		{"zero target", `{"degradation_action":{"target_concurrency":0}}`, 400},
		{"wildcard model", `{"degradation_action":{"models":["gpt-*"]}}`, 400},
		{"save", `{"degradation_action":{"enabled":true,"threshold_percent":50,"target_concurrency":10,"models":["gpt-6-luna"]}}`, 200},
		{"legacy client", `{"priority":7}`, 200},
		{"stale revision", `{"revision":"stale","priority":8}`, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := repo.raw
			out := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(out, req)
			require.Equal(t, tc.status, out.Code, out.Body.String())
			if tc.status != 200 {
				require.Equal(t, before, repo.raw)
			}
		})
	}
	out := httptest.NewRecorder()
	router.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/config", nil))
	require.Equal(t, http.StatusOK, out.Code)
	var got struct {
		Data service.OAuthAutoConfig `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Body.Bytes(), &got))
	require.True(t, got.Data.DegradationAction.Enabled)
	require.Equal(t, 50.0, *got.Data.DegradationAction.ThresholdPercent)
	require.Equal(t, []string{"gpt-6-luna"}, got.Data.DegradationAction.Models)
	require.Equal(t, cfg.ExcelBPS, got.Data.ExcelBPS)
	require.Equal(t, 7, got.Data.Priority)
}
