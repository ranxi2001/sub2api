//go:build unit

package admin

import (
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

func TestAutoConfigSelectedModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := service.DefaultOAuthAutoConfig()
	raw, err := json.Marshal(original)
	require.NoError(t, err)
	repo := &opsHandlerWritableSettings{raw: string(raw)}
	cfg := &config.Config{}
	cfg.Runtime.Role = config.RuntimeRoleGateway
	svc := service.ProvideAccountOpsService(repo, opsHandlerQueue{}, nil, nil, modelBillingHandlerGroups{}, cfg, nil, nil, nil, nil, nil)
	svc.Stop()
	handler := NewAccountOpsHandler(svc, nil)
	router := gin.New()
	router.PUT("/config", handler.SaveAutoConfig)
	router.GET("/config", handler.GetAutoConfig)
	for _, tc := range []struct {
		body   string
		status int
		want   []string
	}{
		{`{"selected_models":["gpt-6.1-sol"]}`, 200, []string{"gpt-6.1-sol"}},
		{`{"concurrency":9}`, 200, []string{"gpt-6.1-sol"}},
		{`{"selected_models":[]}`, 200, []string{}},
		{`{"priority":6}`, 200, []string{}},
		{`{"selected_models":["gpt-*"]}`, 400, nil},
		{`{"selected_models":["duplicate","duplicate"]}`, 400, nil},
		{`{"selected_models":[" model "]}`, 400, nil},
		{`{"selected_models":null}`, 200, nil},
	} {
		before := repo.raw
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(tc.body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		require.Equal(t, tc.status, response.Code, response.Body.String())
		if tc.status != 200 {
			require.Equal(t, before, repo.raw)
			continue
		}
		read := httptest.NewRecorder()
		router.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/config", nil))
		require.Equal(t, 200, read.Code)
		var payload struct {
			Data service.OAuthAutoConfig `json:"data"`
		}
		require.NoError(t, json.Unmarshal(read.Body.Bytes(), &payload))
		require.Equal(t, tc.want, payload.Data.SelectedModels)
		require.Equal(t, original.ExcelBPS, payload.Data.ExcelBPS)
	}
}
