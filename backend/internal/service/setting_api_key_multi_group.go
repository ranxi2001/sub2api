package service

import (
	"context"
	"errors"
)

// APIKeyMultiGroupEnabled reads current policy outside the API-key auth caches.
// A missing legacy setting enables multi-group; an unavailable store cannot
// authorize additional groups. Regional auth carries policy in its signed data.
func (s *SettingService) APIKeyMultiGroupEnabled(ctx context.Context) (bool, error) {
	if s == nil || s.settingRepo == nil {
		return false, ErrAPIKeyGroupPolicyUnavailable
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAPIKeyMultiGroupEnabled)
	if errors.Is(err, ErrSettingNotFound) {
		return true, nil
	}
	if err != nil {
		return false, ErrAPIKeyGroupPolicyUnavailable
	}
	return !isFalseSettingValue(value), nil
}
