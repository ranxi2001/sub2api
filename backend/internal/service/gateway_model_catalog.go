package service

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// gatewayCatalogModels enumerates configured names and platform defaults using
// the same live group, membership and model admission as a forwarded request.
func (s *GatewayService) gatewayCatalogModels(ctx context.Context, groupID *int64, target string) ([]string, error) {
	spec, concrete := domain.LookupPlatform(target)
	reader, _ := s.accountRepo.(GatewayRequestCandidateReader)
	if groupID == nil || *groupID <= 0 || !concrete {
		return nil, nil
	}
	family := spec.Gateway
	if target == PlatformAntigravity {
		family = domain.PlatformGatewayAnthropic
	}
	if snap := s.schedulerSnapshot; snap != nil && snap.localSnapshotReader != nil {
		reader, _ = snap.localSnapshotReader.(GatewayRequestCandidateReader)
		group, err := snap.GetGroupByIDLite(ctx, *groupID)
		if err != nil {
			return nil, err
		}
		ctx = context.WithValue(ctx, ctxkey.Group, group)
		ctx = WithResolvedTargetPlatform(ctx, target)
		ctx = withGatewaySelection(ctx, "catalog", "catalog", family)
	}
	if reader == nil {
		return nil, nil
	}
	candidates, group, err := reader.ListGatewayRequestCandidates(ctx, *groupID, target)
	if err != nil || len(candidates) == 0 || group == nil || group.ID != *groupID {
		return nil, err
	}
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	ctx = WithResolvedTargetPlatform(ctx, target)
	var result []string
	for i := range candidates {
		account := &candidates[i]
		models := groupAllowedConcreteModels(account, groupID)
		for name := range account.GetModelMapping() {
			models = append(models, name)
		}
		if len(models) == 0 {
			models = append(models, group.ModelAllowlist.Models...)
			if !IsMultiProtocolAPIKeyProvider(target) {
				models = append(models, DefaultModelIDsForPlatform(target)...)
			}
		}
		for _, model := range models {
			if model == "" || strings.Contains(model, "*") {
				continue
			}
			modelCtx := withGatewaySelection(ctx, model, model, family)
			if _, err := gatewayProtocolAccountFromSnapshot(modelCtx, account, account, group, family, model); err == nil {
				result = append(result, model)
			}
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

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
	gatewayModels := make(map[string][]string)
	for _, concrete := range platforms {
		names, err := s.gatewayCatalogModels(ctx, &groupID, concrete)
		if err != nil {
			return nil, err
		}
		gatewayModels[concrete] = names
		for _, model := range names {
			seen[model] = struct{}{}
		}
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
		activeRoutes := make([]CompositeModelRoute, 0, len(routes))
		candidates := make(map[string]struct{}, len(seen)+len(routes))
		for model := range seen {
			candidates[model] = struct{}{}
		}
		for _, route := range routes {
			if !route.Enabled {
				continue
			}
			activeRoutes = append(activeRoutes, route)
			if public := strings.TrimSpace(route.PublicModel); public != "" && normalizeCompositeRouteMatchType(route.MatchType) == CompositeRouteMatchExact {
				candidates[public] = struct{}{}
			}
		}
		available := make(map[string]struct{}, len(candidates))
		for model := range candidates {
			// Ordinary discovery covers every endpoint. A selected explicit route
			// overrides account mappings, including when its target is unusable.
			for _, endpoint := range []string{CompositeRouteEndpointAny, CompositeRouteEndpointMessages,
				CompositeRouteEndpointCountTokens, CompositeRouteEndpointResponses, CompositeRouteEndpointChatCompletions,
				CompositeRouteEndpointEmbeddings, CompositeRouteEndpointImages, CompositeRouteEndpointGemini} {
				route, matched := matchCompositeRoute(activeRoutes, model, endpoint)
				if !matched {
					if _, fromAccount := seen[model]; fromAccount {
						available[model] = struct{}{}
						break
					}
					continue
				}
				if route.TargetPlatform == PlatformTypeSafe && endpoint != CompositeRouteEndpointAny {
					continue
				}
				upstream := strings.TrimSpace(route.UpstreamModel)
				if upstream == "" {
					upstream = model
				}
				if slices.Contains(gatewayModels[route.TargetPlatform], upstream) {
					available[model] = struct{}{}
					break
				}
				for _, account := range accounts {
					if account.Platform == route.TargetPlatform && account.IsModelAllowedInGroup(&groupID, upstream) && account.IsModelSupported(upstream) {
						available[model] = struct{}{}
						break
					}
				}
				if _, ok := available[model]; ok {
					break
				}
			}
		}
		seen = available
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models, nil
}
