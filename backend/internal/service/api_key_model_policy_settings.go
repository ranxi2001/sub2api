package service

import (
	"context"
	"strings"
	"unicode"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// AdminUpdateAPIKeySettingsInput contains only administrator-managed settings.
// Nil means preserve; an empty map or empty effort explicitly clears the setting.
type AdminUpdateAPIKeySettingsInput struct {
	GroupID             *int64
	ModelMapping        *map[string]string
	ReasoningEffort     *string
	AllowLongContext    *bool
	ResetRateLimitUsage bool
}

// invalidateAdminAPIKeySettingsCaches runs only after the complete update commits.
func (s *adminServiceImpl) invalidateAdminAPIKeySettingsCaches(ctx context.Context, key *APIKey, resetRateLimit bool) {
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByKey(ctx, key.Key)
	}
	if resetRateLimit && s.billingCacheService != nil {
		_ = s.billingCacheService.InvalidateAPIKeyRateLimit(ctx, key.ID)
	}
}

// Validate rejects ambiguous aliases before any group or quota mutations occur.
func (in AdminUpdateAPIKeySettingsInput) Validate() error {
	if in.GroupID != nil && *in.GroupID < 0 {
		return infraerrors.BadRequest("INVALID_GROUP_ID", "group_id must be non-negative")
	}
	if in.ModelMapping != nil {
		if len(*in.ModelMapping) > 100 {
			return infraerrors.BadRequest("INVALID_API_KEY_MODEL_MAPPING", "model_mapping supports at most 100 entries")
		}
		for source, target := range *in.ModelMapping {
			if !validAPIKeyModelID(source) || !validAPIKeyModelID(target) {
				return infraerrors.BadRequest("INVALID_API_KEY_MODEL_MAPPING", "model IDs must contain 1 to 200 bytes without whitespace or control characters")
			}
		}
	}
	if in.ReasoningEffort != nil {
		switch *in.ReasoningEffort {
		case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		default:
			return infraerrors.BadRequest("INVALID_API_KEY_REASONING_EFFORT", "reasoning_effort must be empty, none, minimal, low, medium, high, xhigh, max, or ultra")
		}
	}
	return nil
}

func validAPIKeyModelID(model string) bool {
	return len(model) > 0 && len(model) <= 200 && strings.IndexFunc(model, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) == -1
}
