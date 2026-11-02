package service

import (
	"context"
	"errors"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	SettingKeyAccountPerformanceWindowMinutes = "account_performance_window_minutes"
	DefaultAccountPerformanceWindowMinutes    = 60
	MaxAccountPerformanceWindowMinutes        = 1440
)

type AccountPerformanceSettings struct {
	WindowMinutes int `json:"window_minutes"`
}

func validateAccountPerformanceWindowMinutes(minutes int) error {
	if minutes < 1 || minutes > MaxAccountPerformanceWindowMinutes {
		return infraerrors.BadRequest("INVALID_ACCOUNT_PERFORMANCE_WINDOW", "Account performance window must be 1–1440 whole minutes")
	}
	return nil
}

// ParseAccountPerformanceWindowMinutes shares defaults and validation between
// admin settings and transactional metrics/evaluation reads. Invalid persisted
// settings fail the read instead of evaluating thresholds with a different window.
func ParseAccountPerformanceWindowMinutes(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultAccountPerformanceWindowMinutes, nil
	}
	minutes, err := strconv.Atoi(value)
	if err != nil {
		return 0, infraerrors.BadRequest("INVALID_ACCOUNT_PERFORMANCE_WINDOW", "Account performance window must be 1–1440 whole minutes")
	}
	if err := validateAccountPerformanceWindowMinutes(minutes); err != nil {
		return 0, err
	}
	return minutes, nil
}

func (s *SettingService) GetAccountPerformanceSettings(ctx context.Context) (*AccountPerformanceSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAccountPerformanceWindowMinutes)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, err
	}
	minutes, err := ParseAccountPerformanceWindowMinutes(value)
	if err != nil {
		return nil, err
	}
	return &AccountPerformanceSettings{WindowMinutes: minutes}, nil
}

func (s *SettingService) SetAccountPerformanceSettings(ctx context.Context, settings AccountPerformanceSettings) error {
	if err := validateAccountPerformanceWindowMinutes(settings.WindowMinutes); err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, SettingKeyAccountPerformanceWindowMinutes, strconv.Itoa(settings.WindowMinutes))
}
