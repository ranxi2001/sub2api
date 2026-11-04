package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func readForecastSettings(t *testing.T, h *SettingHandler) gjson.Result {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return gjson.GetBytes(rec.Body.Bytes(), "data")
}

func TestBalanceForecastSettingsDefaults(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	data := readForecastSettings(t, h)
	require.True(t, data.Get("balance_forecast_enabled").Exists(), "old installations expose the independent forecast switch")
	require.False(t, data.Get("balance_forecast_enabled").Bool())
	require.Equal(t, 12.0, data.Get("balance_forecast_threshold_hours").Float())
	require.EqualValues(t, 3, data.Get("balance_forecast_max_notifications").Int())
	require.NotContains(t, repo.values, "balance_forecast_enabled", "reads do not mutate settings")
}

func TestBalanceForecastSettingsRoundTripAndLegacyOmission(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyBalanceLowNotifyEnabled:     "false",
		service.SettingKeyBalanceLowNotifyThreshold:   "50",
		service.SettingKeyBalanceLowNotifyRechargeURL: "https://recharge.example.test/account",
	})
	rec := doUpdateSettings(t, h, map[string]any{
		"balance_forecast_enabled":           true,
		"balance_forecast_threshold_hours":   6.5,
		"balance_forecast_max_notifications": 2,
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, gjson.GetBytes(rec.Body.Bytes(), "data.balance_forecast_enabled").Bool())
	require.Equal(t, "true", repo.values["balance_forecast_enabled"])
	rec = doUpdateSettings(t, h, map[string]any{"site_name": "Legacy settings client"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// Recreate handler/service to prove values survive the service lifetime.
	h, _ = newStepUpSwitchTestHandler(t, repo.values)
	data := readForecastSettings(t, h)
	require.True(t, data.Get("balance_forecast_enabled").Bool())
	require.Equal(t, 6.5, data.Get("balance_forecast_threshold_hours").Float())
	require.EqualValues(t, 2, data.Get("balance_forecast_max_notifications").Int())
	require.False(t, data.Get("balance_low_notify_enabled").Bool(), "forecast can be on while amount alert is off")
	require.Equal(t, 50.0, data.Get("balance_low_notify_threshold").Float())
	require.Equal(t, "https://recharge.example.test/account", data.Get("balance_low_notify_recharge_url").String())

	rec = doUpdateSettings(t, h, map[string]any{"balance_forecast_enabled": false, "balance_low_notify_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data = readForecastSettings(t, h)
	require.False(t, data.Get("balance_forecast_enabled").Bool())
	require.True(t, data.Get("balance_low_notify_enabled").Bool(), "amount alert remains independent")
	require.Equal(t, 6.5, data.Get("balance_forecast_threshold_hours").Float())
}

func TestBalanceForecastSettingsRejectInvalidWithoutWrites(t *testing.T) {
	for _, field := range []string{"balance_forecast_threshold_hours", "balance_forecast_max_notifications"} {
		values := []any{0, -1, 169, "invalid"}
		if field == "balance_forecast_max_notifications" {
			values = append(values, 1.5, 11)
		}
		for _, value := range values {
			t.Run(field+"/"+fmt.Sprint(value), func(t *testing.T) {
				h, repo := newStepUpSwitchTestHandler(t, map[string]string{"site_name": "unchanged"})
				rec := doUpdateSettings(t, h, map[string]any{field: value, "balance_forecast_enabled": false, "site_name": "must not save"}, nil)
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				require.Equal(t, "unchanged", repo.values["site_name"], "invalid forecast config must reject the whole write")
				require.NotContains(t, repo.values, "balance_forecast_enabled")
			})
		}
	}
}
