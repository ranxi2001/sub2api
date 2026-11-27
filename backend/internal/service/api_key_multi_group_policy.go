package service

import (
	"context"

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
