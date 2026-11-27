package service

import (
	"context"
	"slices"

	"github.com/Wei-Shaw/sub2api/internal/config"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var ErrAPIKeyMultiGroupDisabled = infraerrors.BadRequest("API_KEY_MULTI_GROUP_DISABLED", "This site has disabled API key multi-group binding; select only the primary group")
var ErrAPIKeyPrimaryGroupRequired = infraerrors.Forbidden("API_KEY_PRIMARY_GROUP_REQUIRED", "API key requires a valid primary group while multi-group is disabled")
var ErrAPIKeyGroupPolicyUnavailable = infraerrors.ServiceUnavailable("API_KEY_GROUP_POLICY_UNAVAILABLE", "API key group policy is unavailable")

// APIKeyGroupPolicy is deliberately independent of key cache snapshots.
type APIKeyGroupPolicy interface {
	APIKeyMultiGroupEnabled(context.Context) (bool, error)
}

func (s *APIKeyService) SetGroupPolicy(policy APIKeyGroupPolicy) { s.groupPolicy = policy }

func (s *APIKeyService) multiGroupEnabled(ctx context.Context) (bool, error) {
	if s.groupPolicy == nil {
		return true, nil // legacy constructors; production provider always wires policy
	}
	return s.groupPolicy.APIKeyMultiGroupEnabled(ctx)
}

func (s *APIKeyService) validateGroupSubmission(ctx context.Context, primary *int64, ids []int64) (bool, error) {
	enabled, err := s.multiGroupEnabled(ctx)
	if err != nil {
		return false, err
	}
	if !enabled && (len(ids) > 1 || (primary != nil && len(ids) == 1 && *primary != ids[0])) {
		return false, ErrAPIKeyMultiGroupDisabled
	}
	return enabled, nil
}

// primaryGroupOnly makes a request-local view. Persistent bindings and shared
// auth snapshots retain all groups, so re-enabling policy restores them.
func primaryGroupOnly(key *APIKey) (*APIKey, error) {
	if key == nil || key.GroupID == nil || *key.GroupID <= 0 || key.Group == nil || key.Group.ID != *key.GroupID {
		return nil, ErrAPIKeyPrimaryGroupRequired
	}
	copyKey := *key
	for _, id := range key.GroupIDs {
		if id > 0 && id != *key.GroupID {
			copyKey.primaryOnlyGroupID = *key.GroupID
			break
		}
	}
	copyKey.GroupIDs = []int64{*key.GroupID}
	group := *key.Group
	group.FallbackGroupID = nil
	group.FallbackGroupIDOnInvalidRequest = nil
	copyKey.Group = &group
	return &copyKey, nil
}

func (s *APIKeyService) applyGroupPolicy(ctx context.Context, key *APIKey) (*APIKey, error) {
	if s.localAuth != nil {
		return key, nil
	}
	enabled, err := s.multiGroupEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if enabled {
		return key, nil
	}
	return primaryGroupOnly(key)
}

type apiKeyGroupConstraintKey struct{}

// PrimaryOnlyGroupID is an authentication identity component for queued and WS
// revalidation. A change requires a new request rather than a stale route.
func (k *APIKey) PrimaryOnlyGroupID() int64 {
	if k == nil {
		return 0
	}
	return k.primaryOnlyGroupID
}

// WithAPIKeyGroupConstraint carries only server-derived authorization state.
// An existing restriction cannot be relaxed by a later request-local key view.
func WithAPIKeyGroupConstraint(ctx context.Context, key *APIKey) context.Context {
	if key == nil || key.primaryOnlyGroupID <= 0 {
		return ctx
	}
	if _, restricted := primaryOnlyGroupFromContext(ctx); restricted {
		return ctx
	}
	return context.WithValue(ctx, apiKeyGroupConstraintKey{}, key.primaryOnlyGroupID)
}

func primaryOnlyGroupFromContext(ctx context.Context) (int64, bool) {
	id, _ := ctx.Value(apiKeyGroupConstraintKey{}).(int64)
	return id, id > 0
}

func accountAllowedByAPIKeyGroup(ctx context.Context, account *Account) bool {
	id, restricted := primaryOnlyGroupFromContext(ctx)
	return !restricted || (account != nil && slices.Contains(account.GroupIDs, id))
}

// simpleModeAccountScope preserves platform-wide legacy simple scheduling only
// for requests that were not restricted from an original multi-group binding.
func simpleModeAccountScope(ctx context.Context, cfg *config.Config) bool {
	_, restricted := primaryOnlyGroupFromContext(ctx)
	return cfg != nil && cfg.RunMode == config.RunModeSimple && !restricted
}

// Copy before filtering: scheduler/auth cache slices are shared with other keys.
func filterAPIKeyGroupAccounts(ctx context.Context, accounts []Account) []Account {
	if _, restricted := primaryOnlyGroupFromContext(ctx); !restricted {
		return accounts
	}
	filtered := make([]Account, 0, len(accounts))
	for i := range accounts {
		if accountAllowedByAPIKeyGroup(ctx, &accounts[i]) {
			filtered = append(filtered, accounts[i])
		}
	}
	return filtered
}
