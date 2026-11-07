package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// GetGroupModelCatalog is the fail-closed source for multi-group discovery.
// Unlike the legacy listing API, an error or missing accounts never means
// "advertise defaults". One repository snapshot supplies every concrete platform.
func (s *GatewayService) GetGroupModelCatalog(ctx context.Context, groupID int64, platform string) ([]string, error) {
	if s == nil || s.accountRepo == nil || groupID <= 0 {
		return nil, fmt.Errorf("model discovery is unavailable")
	}
	accounts, err := s.accountRepo.ListSchedulableByGroupID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	platforms := []string{platform}
	if platform == PlatformComposite {
		platforms = domain.CompositePrecedencePlatformIDs()
	}
	seen := make(map[string]struct{})
	for _, concrete := range platforms {
		var visible []Account
		for _, account := range accounts {
			if account.Platform == concrete || mixedListingAccountAllowed(concrete, &account) {
				visible = append(visible, account)
			}
		}
		if len(visible) == 0 {
			continue
		}
		models := availableModelsFromAccounts(visible, &groupID, concrete)
		if models == nil && !IsMultiProtocolAPIKeyProvider(concrete) {
			// Defaults are a source only for an actual account that can expose
			// the model; group restrictions remain authoritative even when empty.
			for _, model := range DefaultModelIDsForPlatform(concrete) {
				for _, account := range visible {
					if account.Platform != concrete && !mixedListingModelAllowed(concrete, model) {
						continue
					}
					if account.IsModelAllowedInGroup(&groupID, model) && account.IsModelSupported(model) {
						models = append(models, model)
						break
					}
				}
			}
		}
		if concrete == PlatformOpenAI {
			models = filterRetiredOpenAIModelIDs(visible, models)
		}
		for _, model := range models {
			for _, account := range visible {
				if account.Platform != concrete && !mixedListingModelAllowed(concrete, model) {
					continue
				}
				if account.IsModelAllowedInGroup(&groupID, model) && account.IsModelSupported(model) {
					seen[model] = struct{}{}
					break
				}
			}
		}
	}
	if platform == PlatformComposite && s.compositeResolver != nil && s.compositeResolver.repo != nil {
		routes, err := s.compositeResolver.repo.ListByGroup(ctx, groupID, false)
		if err != nil {
			return nil, err
		}
		for _, route := range routes {
			public := strings.TrimSpace(route.PublicModel)
			if !route.Enabled || route.MatchType != CompositeRouteMatchExact || public == "" {
				continue
			}
			upstream := strings.TrimSpace(route.UpstreamModel)
			if upstream == "" {
				upstream = public
			}
			// An alias is a source only when its concrete target is usable in
			// this group. A route declaration alone cannot revive an empty group.
			for _, account := range accounts {
				if account.Platform == route.TargetPlatform && account.IsModelAllowedInGroup(&groupID, upstream) && account.IsModelSupported(upstream) {
					seen[public] = struct{}{}
					break
				}
			}
		}
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models, nil
}
