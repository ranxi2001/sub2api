package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSmartOpsSidebarSettingsRoundTripAndOmission(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	get := func() string {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
		h.GetSettings(c)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return gjson.Get(rec.Body.String(), "data.smart_ops_hidden_menu_items").Raw
	}
	defaults := get()
	require.JSONEq(t, `["/admin/lottery","/admin/channel-operations","/admin/controlled-experiments","/admin/account-ops","/admin/token-guard","/admin/group-cache-tests","/admin/request-captures","/admin/harvest-flow"]`, defaults, "unconfigured installations display only the six default entries")
	rec := doUpdateSettings(t, h, map[string]any{"site_name": "example"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, repo.values, service.SettingKeySmartOpsHiddenMenuItems, "omitted fields remain unconfigured")
	require.JSONEq(t, defaults, get(), "older clients retain the defaults when saving other settings")
	require.JSONEq(t, defaults, gjson.Get(rec.Body.String(), "data.smart_ops_hidden_menu_items").Raw)
	rec = doUpdateSettings(t, h, map[string]any{"smart_ops_hidden_menu_items": []string{"/admin/token-guard", "/admin/token-guard", "/admin/harvest-flow"}}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `["/admin/token-guard","/admin/harvest-flow"]`, get())
	require.JSONEq(t, get(), gjson.Get(rec.Body.String(), "data.smart_ops_hidden_menu_items").Raw)
	saved := repo.values[service.SettingKeySmartOpsHiddenMenuItems]
	rec = doUpdateSettings(t, h, map[string]any{"site_name": "example"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, saved, repo.values[service.SettingKeySmartOpsHiddenMenuItems], "older clients must not clear visibility preferences")
	rec = doUpdateSettings(t, h, map[string]any{"smart_ops_hidden_menu_items": []string{}}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `[]`, get(), "explicit empty array restores all entries")
	rec = doUpdateSettings(t, h, map[string]any{"site_name": "example"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `[]`, get(), "older clients retain an explicit choice to show all entries")
}
