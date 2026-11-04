package service

import (
	"math"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	defaultBalanceForecastThresholdHours   = 12
	defaultBalanceForecastMaxNotifications = 3
)

func parseBalanceForecastSettings(values map[string]string, result *SystemSettings) {
	result.BalanceForecastEnabled = values[SettingKeyBalanceForecastEnabled] == "true"
	result.BalanceForecastThresholdHours = defaultBalanceForecastThresholdHours
	result.BalanceForecastMaxNotifications = defaultBalanceForecastMaxNotifications
	if raw, exists := values[SettingKeyBalanceForecastThresholdHours]; exists {
		value, err := strconv.ParseFloat(raw, 64)
		if err == nil && value > 0 && value <= 168 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			result.BalanceForecastThresholdHours = value
		} else {
			result.BalanceForecastEnabled = false
		}
	}
	if raw, exists := values[SettingKeyBalanceForecastMaxNotifications]; exists {
		value, err := strconv.Atoi(raw)
		if err == nil && value > 0 && value <= 10 {
			result.BalanceForecastMaxNotifications = value
		} else {
			result.BalanceForecastEnabled = false
		}
	}
}

func (s *SystemSettings) normalizeBalanceForecastSettings() error {
	// Older in-process whole-document callers predate these fields. HTTP
	// callers merge omissions with parsed defaults and reject explicit zeros.
	if !s.BalanceForecastEnabled && s.BalanceForecastThresholdHours == 0 && s.BalanceForecastMaxNotifications == 0 {
		s.BalanceForecastThresholdHours = defaultBalanceForecastThresholdHours
		s.BalanceForecastMaxNotifications = defaultBalanceForecastMaxNotifications
	}
	if s.BalanceForecastThresholdHours <= 0 || s.BalanceForecastThresholdHours > 168 || math.IsNaN(s.BalanceForecastThresholdHours) || math.IsInf(s.BalanceForecastThresholdHours, 0) {
		return infraerrors.BadRequest("INVALID_BALANCE_FORECAST_THRESHOLD_HOURS", "balance_forecast_threshold_hours must be finite, positive and at most 168")
	}
	if s.BalanceForecastMaxNotifications <= 0 || s.BalanceForecastMaxNotifications > 10 {
		return infraerrors.BadRequest("INVALID_BALANCE_FORECAST_MAX_NOTIFICATIONS", "balance_forecast_max_notifications must be an integer from 1 to 10")
	}
	return nil
}
