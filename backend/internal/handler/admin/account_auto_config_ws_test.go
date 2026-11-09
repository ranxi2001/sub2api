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

func TestAutoConfigWSSaveAndLegacyClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := service.DefaultOAuthAutoConfig()
	original.OpenAIWSMode, original.OpenAIWSSSEAcceleration = nil, nil
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
		name, body, mode  string
		status            int
		acceleration      bool
		modeSet, accelSet bool
	}{
		{name: "legacy template and client keep fields absent", body: `{"priority":6}`, status: http.StatusOK},
		{name: "save default context pool independently", body: `{"openai_ws_mode":"ctx_pool"}`, status: http.StatusOK, mode: "ctx_pool", modeSet: true},
		{name: "enable SSE independently", body: `{"openai_ws_sse_acceleration":true}`, status: http.StatusOK, mode: "ctx_pool", modeSet: true, acceleration: true, accelSet: true},
		{name: "old client preserves selected settings", body: `{"priority":7}`, status: http.StatusOK, mode: "ctx_pool", modeSet: true, acceleration: true, accelSet: true},
		{name: "explicit off and false", body: `{"openai_ws_mode":"off","openai_ws_sse_acceleration":false}`, status: http.StatusOK, mode: "off", modeSet: true, accelSet: true},
		{name: "passthrough mode", body: `{"openai_ws_mode":"passthrough"}`, status: http.StatusOK, mode: "passthrough", modeSet: true, accelSet: true},
		{name: "HTTP bridge mode", body: `{"openai_ws_mode":"http_bridge"}`, status: http.StatusOK, mode: "http_bridge", modeSet: true, accelSet: true},
		{name: "reject unsupported mode", body: `{"openai_ws_mode":"shared"}`, status: http.StatusBadRequest, mode: "http_bridge", modeSet: true, accelSet: true},
		{name: "reject empty mode", body: `{"openai_ws_mode":""}`, status: http.StatusBadRequest, mode: "http_bridge", modeSet: true, accelSet: true},
		{name: "reject mode type", body: `{"openai_ws_mode":false}`, status: http.StatusBadRequest, mode: "http_bridge", modeSet: true, accelSet: true},
		{name: "reject acceleration type", body: `{"openai_ws_sse_acceleration":"true"}`, status: http.StatusBadRequest, mode: "http_bridge", modeSet: true, accelSet: true},
		{name: "null restores unmanaged fields", body: `{"openai_ws_mode":null,"openai_ws_sse_acceleration":null}`, status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := repo.raw
			written := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(written, request)
			require.Equal(t, tc.status, written.Code, written.Body.String())
			if tc.status != http.StatusOK {
				require.Equal(t, before, repo.raw, "invalid WS values must not change stored settings")
			}
			read := httptest.NewRecorder()
			router.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/config", nil))
			require.Equal(t, http.StatusOK, read.Code)
			var payload struct {
				Data service.OAuthAutoConfig `json:"data"`
			}
			require.NoError(t, json.Unmarshal(read.Body.Bytes(), &payload))
			if tc.modeSet {
				require.NotNil(t, payload.Data.OpenAIWSMode)
				require.Equal(t, tc.mode, *payload.Data.OpenAIWSMode)
			} else {
				require.Nil(t, payload.Data.OpenAIWSMode)
			}
			if tc.accelSet {
				require.NotNil(t, payload.Data.OpenAIWSSSEAcceleration)
				require.Equal(t, tc.acceleration, *payload.Data.OpenAIWSSSEAcceleration)
			} else {
				require.Nil(t, payload.Data.OpenAIWSSSEAcceleration)
			}
			require.Equal(t, original.ExcelBPS, payload.Data.ExcelBPS, "WS changes must preserve the independent BPS template")
		})
	}
}
