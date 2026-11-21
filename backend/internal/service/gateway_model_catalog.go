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

type gatewayModelCatalog struct {
	ctx      context.Context
	family   domain.PlatformGateway
	group    *Group
	accounts []Account
}

func (catalog gatewayModelCatalog) allows(public, upstream string) bool {
	if catalog.group == nil || public == "" || upstream == "" {
		return false
	}
	ctx := withGatewaySelection(catalog.ctx, public, upstream, catalog.family)
	for i := range catalog.accounts {
		account := &catalog.accounts[i]
		if _, err := gatewayProtocolAccountFromSnapshot(ctx, account, account, catalog.group, catalog.family, upstream); err == nil {
			return true
		}
	}
	return false
}

func (s *GatewayService) readGatewayModelCatalog(ctx context.Context, groupID *int64, target string) (gatewayModelCatalog, error) {
	spec, concrete := domain.LookupPlatform(target)
	reader, _ := s.accountRepo.(GatewayRequestCandidateReader)
	if groupID == nil || *groupID <= 0 || !concrete {
		return gatewayModelCatalog{}, nil
	}
	family := spec.Gateway
	if target == PlatformAntigravity {
		family = domain.PlatformGatewayAnthropic
	}
	if snap := s.schedulerSnapshot; snap != nil && snap.localSnapshotReader != nil {
		reader, _ = snap.localSnapshotReader.(GatewayRequestCandidateReader)
		group, err := snap.GetGroupByIDLite(ctx, *groupID)
		if err != nil {
			return gatewayModelCatalog{}, err
		}
		ctx = context.WithValue(ctx, ctxkey.Group, group)
		ctx = WithResolvedTargetPlatform(ctx, target)
		ctx = withGatewaySelection(ctx, "catalog", "catalog", family)
	}
	if reader == nil {
		return gatewayModelCatalog{}, nil
	}
	candidates, group, err := reader.ListGatewayRequestCandidates(ctx, *groupID, target)
	if err != nil || len(candidates) == 0 || group == nil || group.ID != *groupID {
		return gatewayModelCatalog{}, err
	}
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	ctx = WithResolvedTargetPlatform(ctx, target)
	return gatewayModelCatalog{ctx: ctx, family: family, group: group, accounts: candidates}, nil
}

func (catalog gatewayModelCatalog) directModels(target string) []string {
	if catalog.group == nil {
		return nil
	}
	groupID := &catalog.group.ID
	group := catalog.group
	var result []string
	for i := range catalog.accounts {
		account := &catalog.accounts[i]
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
			modelCtx := withGatewaySelection(catalog.ctx, model, model, catalog.family)
			if _, err := gatewayProtocolAccountFromSnapshot(modelCtx, account, account, group, catalog.family, model); err == nil {
				result = append(result, model)
			}
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
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
	var routes []CompositeModelRoute
	if platform == PlatformComposite && s.compositeResolver != nil && s.compositeResolver.repo != nil {
		routes, err = s.compositeResolver.repo.ListByGroup(ctx, groupID, false)
		if err != nil {
			return nil, err
		}
	}
	gatewayCatalogs := make(map[string]gatewayModelCatalog)
	for _, concrete := range platforms {
		catalog, err := s.readGatewayModelCatalog(ctx, &groupID, concrete)
		if err != nil {
			return nil, err
		}
		gatewayCatalogs[concrete] = catalog
		for _, model := range catalog.directModels(concrete) {
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
	if len(routes) > 0 {
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
				if gatewayCatalogs[route.TargetPlatform].allows(model, upstream) {
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
