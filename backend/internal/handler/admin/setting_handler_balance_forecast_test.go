package admin

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBalanceForecastRetiredSettings(t *testing.T) {
	legacy := map[string]string{
		"balance_forecast_enabled":                  "true",
		"balance_forecast_threshold_hours":          "6.5",
		"balance_forecast_max_notifications":        "2",
		service.SettingKeyBalanceLowNotifyEnabled:   "true",
		service.SettingKeyBalanceLowNotifyThreshold: "50",
	}
	h, repo := newStepUpSwitchTestHandler(t, maps.Clone(legacy))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := gjson.GetBytes(rec.Body.Bytes(), "data")
	for _, field := range []string{"balance_forecast_enabled", "balance_forecast_threshold_hours", "balance_forecast_max_notifications"} {
		require.False(t, data.Get(field).Exists(), field)
	}
	require.True(t, data.Get("balance_low_notify_enabled").Bool())

	// Stale clients may still send the retired fields. Ignore them without
	// validating or persisting them, while applying supported settings.
	rec = doUpdateSettings(t, h, map[string]any{
		"balance_forecast_enabled":           false,
		"balance_forecast_threshold_hours":   "retired field",
		"balance_forecast_max_notifications": -1,
		"balance_low_notify_threshold":       25,
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data = gjson.GetBytes(rec.Body.Bytes(), "data")
	require.False(t, data.Get("balance_forecast_enabled").Exists())
	require.True(t, data.Get("balance_low_notify_enabled").Bool())
	require.Equal(t, 25.0, data.Get("balance_low_notify_threshold").Float())
	for _, field := range []string{"balance_forecast_enabled", "balance_forecast_threshold_hours", "balance_forecast_max_notifications"} {
		require.Equal(t, legacy[field], repo.values[field], "historical settings are retained but unused")
	}
}
