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

func TestAutoConfigDisableScheduling429SaveAndLegacyClient(t *testing.T) {
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
		name       string
		body       string
		status     int
		disable    bool
		rotateTOTP bool
	}{
		{"enable both", `{"openai_disable_scheduling_on_429":true,"openai_totp_rotate_on_429":true}`, http.StatusOK, true, true},
		{"old client omits flags", `{"priority":6}`, http.StatusOK, true, true},
		{"disable rotation independently", `{"openai_totp_rotate_on_429":false}`, http.StatusOK, true, false},
		{"reject non boolean", `{"openai_disable_scheduling_on_429":"true"}`, http.StatusBadRequest, true, false},
		{"explicit opt out", `{"openai_disable_scheduling_on_429":false}`, http.StatusOK, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := repo.raw
			written := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(written, request)
			require.Equal(t, tc.status, written.Code, written.Body.String())
			if tc.status != http.StatusOK {
				require.Equal(t, before, repo.raw, "invalid values must not change stored settings")
			}
			read := httptest.NewRecorder()
			router.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/config", nil))
			require.Equal(t, http.StatusOK, read.Code)
			var payload struct {
				Data service.OAuthAutoConfig `json:"data"`
			}
			require.NoError(t, json.Unmarshal(read.Body.Bytes(), &payload))
			require.Equal(t, tc.disable, payload.Data.OpenAIDisableSchedulingOn429)
			require.Equal(t, tc.rotateTOTP, payload.Data.OpenAITOTPRotateOn429)
			require.Equal(t, original.ExcelBPS, payload.Data.ExcelBPS)
		})
	}
}
