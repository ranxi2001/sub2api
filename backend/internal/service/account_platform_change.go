package service

import (
	"context"
	"maps"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type accountPlatformChangeKey struct{}

// WithAccountPlatformChange carries the validated platform transition through
// the transaction to the repository's metadata merge.
func WithAccountPlatformChange(ctx context.Context) context.Context {
	return context.WithValue(ctx, accountPlatformChangeKey{}, true)
}

// AccountPlatformChanging marks an explicit admin platform edit. Repository
// merges must discard the old platform's runtime metadata after taking the lock.
func AccountPlatformChanging(ctx context.Context) bool {
	changed, _ := ctx.Value(accountPlatformChangeKey{}).(bool)
	return changed
}

// AccountPlatformPortableExtra retains account-wide costs and quota accounting.
// Protocol options, model observations and provider runtime state are not portable.
func AccountPlatformPortableExtra(extra map[string]any) map[string]any {
	out := make(map[string]any)
	for key, value := range extra {
		if strings.HasPrefix(key, "quota_") || key == AccountCostMultiplierExtraKey || key == AccountCostAutoSyncExtraKey || key == TokenProcurementBasisKey || key == ImagePurchasePricesKey {
			out[key] = value
		}
	}
	return out
}

func portablePlatformCredentials(account *Account) map[string]any {
	keys := []string{"api_key", "base_url"}
	if account.Type == AccountTypeServiceAccount {
		keys = []string{"auth_mode", "api_key", "service_account_json", "service_account", "private_key", "project_id", "client_email", "location", "vertex_location", "tier_id"}
	}
	out := make(map[string]any)
	for _, key := range keys {
		if value, ok := account.Credentials[key]; ok {
			out[key] = value
		}
	}
	return out
}

// The editor receives redacted credentials. An omitted auth_mode must retain
// the stored mode, including when other credential settings are being replaced.
// Validate it before platform preparation or any account/group writes.
func prepareVertexAuthModeUpdate(current *Account, input *UpdateAccountInput) error {
	if current.Type != AccountTypeServiceAccount || (current.Platform != PlatformGemini && current.Platform != PlatformAnthropic) {
		return nil
	}
	mode, explicit := input.Credentials["auth_mode"]
	if !explicit {
		var exists bool
		mode, exists = current.Credentials["auth_mode"]
		if !exists {
			return nil // Legacy Service Account JSON credentials have no marker.
		}
	}
	authMode, ok := mode.(string)
	if !ok || (strings.TrimSpace(authMode) != vertexAuthModeAPIKey && strings.TrimSpace(authMode) != "service_account") {
		return infraerrors.BadRequest("INVALID_VERTEX_AUTH_MODE", "unsupported Vertex authentication mode")
	}
	platform := current.Platform
	if input.Platform != "" {
		platform = input.Platform
	}
	if strings.TrimSpace(authMode) == vertexAuthModeAPIKey && platform != PlatformGemini {
		return infraerrors.BadRequest("ACCOUNT_PLATFORM_BOUND", "Vertex API key authentication is only supported on Gemini; Anthropic requires Service Account JSON")
	}
	if !explicit && len(input.Credentials) > 0 {
		input.Credentials = maps.Clone(input.Credentials)
		input.Credentials["auth_mode"] = mode
	}
	return nil
}

func (s *adminServiceImpl) prepareAccountPlatformChange(ctx context.Context, current *Account, input *UpdateAccountInput) (*Account, error) {
	if !domain.IsConcretePlatform(input.Platform) {
		return nil, infraerrors.BadRequest("INVALID_ACCOUNT_PLATFORM", "unknown account platform")
	}
	if current.IsShadow() {
		return nil, infraerrors.BadRequest("ACCOUNT_PLATFORM_BOUND", "shadow accounts inherit their platform from the parent")
	}
	effectiveType := current.Type
	if input.Type != "" {
		effectiveType = input.Type
	}
	compatible := effectiveType == current.Type && ((current.Type == AccountTypeAPIKey && input.Platform != PlatformAntigravity) ||
		(current.Type == AccountTypeServiceAccount && (current.Platform == PlatformAnthropic || current.Platform == PlatformGemini) &&
			(input.Platform == PlatformAnthropic || input.Platform == PlatformGemini)))
	if !compatible {
		return nil, infraerrors.BadRequest("ACCOUNT_PLATFORM_BOUND", "this credential type is bound to its platform; create and authorize an account on the target platform")
	}
	shadows, err := s.accountRepo.ListShadowsByParent(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	if len(shadows) > 0 {
		return nil, infraerrors.BadRequest("ACCOUNT_PLATFORM_BOUND", "delete dependent shadow accounts before changing the parent platform")
	}
	// Platform and final group bindings are validated before any write. Explicit
	// incompatible assignments are rejected; omitted groups keep compatible ones.
	groupIDs := current.GroupIDs
	if input.GroupIDs != nil {
		groupIDs = *input.GroupIDs
	}
	compatibleIDs := make([]int64, 0, len(groupIDs))
	for _, id := range groupIDs {
		if s.groupRepo == nil {
			return nil, infraerrors.BadRequest("ACCOUNT_PLATFORM_GROUP", "group repository is required to change account platform")
		}
		group, err := s.groupRepo.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if group.Platform != input.Platform && group.Platform != PlatformComposite {
			if input.GroupIDs != nil {
				return nil, infraerrors.BadRequest("ACCOUNT_PLATFORM_GROUP", "selected group is incompatible with the target platform")
			}
			continue
		}
		compatibleIDs = append(compatibleIDs, id)
	}
	input.GroupIDs = &compatibleIDs
	account := *current
	account.Platform = input.Platform
	account.Credentials = portablePlatformCredentials(current)
	account.Extra = AccountPlatformPortableExtra(current.Extra)
	if input.Extra != nil {
		// The form already renders target-platform settings. Copy the request so
		// normalization never mutates a caller's reusable DTO or stored snapshot.
		input.Extra = maps.Clone(input.Extra)
	}
	return &account, nil
}
