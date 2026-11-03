package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const maxAPIKeyGroups = 20

// APIKeyGroupAvailability describes a snapshot, not a reservation. The selected
// group's normal admission and account scheduler still own the final checks.
type APIKeyGroupAvailability struct {
	Available bool
	Busy      bool
	Load      float64
}

type APIKeyGroupRouter interface {
	ProbeAPIKeyGroup(context.Context, *Group, string, string) (APIKeyGroupAvailability, error)
}

// SetGroupRouter is called once during gateway route registration.
func (s *APIKeyService) SetGroupRouter(router APIKeyGroupRouter) { s.groupRouter = router }

func (k *APIKey) ConfiguredGroupIDs() []int64 {
	if k == nil {
		return nil
	}
	if len(k.GroupIDs) > 0 {
		return append([]int64(nil), k.GroupIDs...)
	}
	if k.GroupID != nil {
		return []int64{*k.GroupID}
	}
	return []int64{}
}

func normalizeAPIKeyGroupIDs(primary *int64, ids []int64) ([]int64, error) {
	if len(ids) > maxAPIKeyGroups {
		return nil, infraerrors.BadRequest("INVALID_API_KEY_GROUPS", "At most 20 groups may be selected")
	}
	if len(ids) == 0 && primary != nil {
		ids = []int64{*primary}
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, infraerrors.BadRequest("INVALID_API_KEY_GROUPS", "Group IDs must be positive and unique")
		}
		seen[id] = true
	}
	if primary != nil && len(ids) > 0 && ids[0] != *primary {
		return nil, infraerrors.BadRequest("INVALID_API_KEY_GROUPS", "group_id must match the first group_ids entry")
	}
	return append([]int64(nil), ids...), nil
}

func (s *APIKeyService) validateAPIKeyGroups(ctx context.Context, user *User, ids []int64) error {
	for _, id := range ids {
		group, err := s.groupRepo.GetByID(ctx, id)
		if err != nil {
			return fmt.Errorf("get group: %w", err)
		}
		if (len(ids) > 1 && !group.IsActive()) || !s.canUserBindGroup(ctx, user, group) {
			return ErrGroupNotAllowed
		}
	}
	return nil
}

// PinAPIKeyGroup preserves a request's chosen billing identity across queue/WS
// revalidation. Never reselect underneath a request that already reserved funds.
func (s *APIKeyService) PinAPIKeyGroup(ctx context.Context, key *APIKey, id int64) (*APIKey, error) {
	bound := false
	for _, candidate := range key.ConfiguredGroupIDs() {
		if candidate == id {
			bound = true
			break
		}
	}
	if !bound {
		return nil, infraerrors.ServiceUnavailable("API_KEY_GROUP_CHANGED", "API key groups changed; please retry")
	}
	group, err := s.groupRepo.GetByIDLite(ctx, id)
	if err != nil {
		return nil, err
	}
	if key.User == nil || !group.IsActive() || !s.canUserBindGroup(ctx, key.User, group) {
		return nil, ErrGroupNotAllowed
	}
	return s.apiKeyWithGroup(ctx, key, group)
}

func (s *APIKeyService) apiKeyWithGroup(ctx context.Context, key *APIKey, group *Group) (*APIKey, error) {
	copyKey := *key
	copyKey.ModelMapping = maps.Clone(key.ModelMapping)
	copyUser := *key.User
	copyKey.User = &copyUser
	id := group.ID
	copyKey.GroupID, copyKey.Group = &id, group
	copyUser.UserGroupRPMOverride = nil
	copyUser.UserGroupDeniedModels = nil
	if s.userGroupRateRepo != nil {
		var err error
		copyUser.UserGroupRPMOverride, err = s.userGroupRateRepo.GetRPMOverrideByUserAndGroup(ctx, key.UserID, id)
		if err != nil {
			return nil, err
		}
		if err := s.loadUserGroupDeniedModels(ctx, &copyKey); err != nil {
			return nil, err
		}
	}
	return &copyKey, nil
}

// BillingGroups exposes only groups bound to this key and still permitted for
// its owner. Inactive groups remain visible with their status in the declaration.
func (s *APIKeyService) BillingGroups(ctx context.Context, key *APIKey) ([]*APIKey, error) {
	var out []*APIKey
	for _, id := range key.ConfiguredGroupIDs() {
		group, err := s.groupRepo.GetByIDLite(ctx, id)
		if errors.Is(err, ErrGroupNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !s.canUserBindGroup(ctx, key.User, group) {
			continue
		}
		copyKey := *key
		copyKey.ModelMapping = maps.Clone(key.ModelMapping)
		copyKey.GroupID, copyKey.Group = &group.ID, group
		out = append(out, &copyKey)
	}
	return out, nil
}

type apiKeyGroupCandidate struct {
	key          *APIKey
	rate         float64
	availability APIKeyGroupAvailability
}

// SelectRequestGroup prefers immediately available groups, then the lowest
// effective token multiplier (including user overrides and peak hours). Equal
// rates prefer lower load, with randomized ties to avoid a permanent hot group.
func (s *APIKeyService) SelectRequestGroup(ctx context.Context, key *APIKey, models []string, kind string, subscriptions *SubscriptionService, accepts func(*APIKey) bool, checkBilling bool) (*APIKey, error) {
	if key == nil || len(key.ConfiguredGroupIDs()) < 2 {
		return key, nil
	}
	if s.groupRouter == nil || s.localAuth != nil {
		return nil, infraerrors.ServiceUnavailable("MULTI_GROUP_ROUTING_UNAVAILABLE", "Multi-group routing is unavailable on this gateway")
	}
	var candidates []apiKeyGroupCandidate
	now := time.Now()
	var lastErr error
	for _, id := range key.ConfiguredGroupIDs() {
		group, err := s.groupRepo.GetByIDLite(ctx, id)
		if err != nil {
			if !errors.Is(err, ErrGroupNotFound) {
				return nil, err
			}
			continue
		}
		if !group.IsActive() {
			continue
		}
		if group.IsSubscriptionType() {
			if s.userSubRepo == nil {
				continue
			}
			sub, subErr := s.userSubRepo.GetActiveByUserIDAndGroupID(ctx, key.UserID, id)
			if subErr != nil {
				if !errors.Is(subErr, ErrSubscriptionNotFound) && !errors.Is(subErr, ErrSubscriptionExpired) {
					return nil, subErr
				}
				continue
			}
			if checkBilling && subscriptions != nil {
				// Validation adjusts expired windows in memory; normal auth performs
				// authoritative maintenance after the group is selected.
				copySub := *sub
				if _, subErr := subscriptions.ValidateAndCheckLimits(&copySub, group); subErr != nil {
					lastErr = subErr
					continue
				}
			}
		} else {
			if !key.User.CanBindGroup(id, group.IsExclusive) {
				continue
			}
		}
		candidate, err := s.apiKeyWithGroup(ctx, key, group)
		if err != nil {
			return nil, err
		}
		permitted := true
		for _, model := range models {
			if group.ModelAllowlistEnabled() && !group.ModelAllowlist.Allows(model) {
				permitted = false
				break
			}
		}
		if !permitted || FirstUserGroupDeniedModel(candidate.DeniedModelsInGroup(), models) != "" || (accepts != nil && !accepts(candidate)) {
			continue
		}
		rate := group.RateMultiplier
		if s.userGroupRateRepo != nil {
			userRate, err := s.userGroupRateRepo.GetByUserAndGroup(ctx, key.UserID, id)
			if err != nil {
				return nil, err
			}
			if userRate != nil {
				rate = *userRate
			}
		}
		switch kind {
		case "image":
			rate = resolveImageRateMultiplier(candidate, rate)
		case "video":
			rate = resolveVideoRateMultiplier(candidate, rate)
		default:
			rate *= group.PeakMultiplierAt(now)
		}
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			continue
		}
		candidates = append(candidates, apiKeyGroupCandidate{key: candidate, rate: rate})
	}
	if len(candidates) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, infraerrors.Forbidden("NO_ELIGIBLE_API_KEY_GROUP", "No selected group permits this request or has usable billing entitlement")
	}
	// Shuffle before stable sorting so exact ties share traffic.
	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].rate < candidates[j].rate })
	var best *apiKeyGroupCandidate
	for i := range candidates {
		candidate := &candidates[i]
		model := ""
		if len(models) > 0 {
			model = models[0]
		}
		availability, err := s.groupRouter.ProbeAPIKeyGroup(ctx, candidate.key.Group, model, kind)
		if err != nil {
			lastErr = err
			continue
		}
		if !availability.Available {
			continue
		}
		candidate.availability = availability
		if best == nil || apiKeyGroupLess(*candidate, *best) {
			best = candidate
		}
		// Every remaining group costs more and cannot beat an idle group.
		if best != nil && !best.availability.Busy && i+1 < len(candidates) && candidates[i+1].rate > best.rate {
			break
		}
	}
	if best == nil {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, infraerrors.ServiceUnavailable("NO_AVAILABLE_API_KEY_GROUP", "No selected group currently has accounts available for this model")
	}
	return best.key, nil
}

func apiKeyGroupLess(a, b apiKeyGroupCandidate) bool {
	if a.availability.Busy != b.availability.Busy {
		return !a.availability.Busy
	}
	if a.rate != b.rate {
		return a.rate < b.rate
	}
	return a.availability.Load < b.availability.Load
}
