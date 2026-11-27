//go:build unit

package admin

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIssue1081SettingRoundTripAndLegacyOmission(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{service.SettingKeySiteName: "Fixture"})
	for _, enabled := range []bool{false, true, false} {
		rec := doUpdateSettings(t, h, map[string]any{"api_key_multi_group_enabled": enabled}, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		want := "false"
		if enabled {
			want = "true"
		}
		require.Equal(t, want, repo.values[service.SettingKeyAPIKeyMultiGroupEnabled])
		rec = doUpdateSettings(t, h, map[string]any{"site_name": "Fixture"}, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, want, repo.values[service.SettingKeyAPIKeyMultiGroupEnabled])
	}
}

func TestIssue1081AdminExplicitMultiRejected(t *testing.T) {
	settings, _ := newStepUpSwitchTestHandler(t, map[string]string{service.SettingKeyAPIKeyMultiGroupEnabled: "false"})
	h := NewAdminAPIKeyHandler(nil, settings.settingService)
	for _, body := range []string{`{"group_ids":[1,2]}`, `{"group_id":1,"group_ids":[2]}`} {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.PUT("/api-keys/:id", h.UpdateGroup)
		req := httptest.NewRequest(http.MethodPut, "/api-keys/11", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "API_KEY_MULTI_GROUP_DISABLED")
	}
}
