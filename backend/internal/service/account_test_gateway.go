package service

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
)

type gatewayAccountTestKey struct{}

// IsGatewayAccountTest identifies an observational probe of a shared gateway.
// Neither upstream failures nor a successful probe change its scheduling state.
func IsGatewayAccountTest(ctx context.Context) bool {
	probe, _ := ctx.Value(gatewayAccountTestKey{}).(bool)
	return probe
}

func (s *AccountTestService) testGatewayAccountConnection(c *gin.Context, selected *Account, model, prompt, mode string, opts AccountTestOptions) error {
	ctx := context.WithValue(c.Request.Context(), gatewayAccountTestKey{}, true)
	c.Request = c.Request.WithContext(ctx)
	model = strings.TrimSpace(model)
	if model == "" || (mode != "" && mode != AccountTestModeDefault && mode != AccountTestModeGrokText) || opts.ImageDataURL != "" || opts.AudioDataURL != "" {
		return s.sendErrorAndEnd(c, "Gateway connection tests require a selected text model and default mode")
	}
	groupID := opts.GroupID
	if groupID < 0 {
		return s.sendErrorAndEnd(c, ErrGatewayAccountGroup.Error())
	}
	if groupID == 0 {
		if group, _ := ctx.Value(ctxkey.Group).(*Group); group != nil {
			groupID = group.ID
		}
	}
	if groupID != 0 && !ObserverCanManageGroup(ctx, groupID) {
		return s.sendErrorAndEnd(c, ErrObserverScope.Error())
	}
	groupIDs := []int64{groupID}
	if groupID == 0 {
		groupIDs = slices.Clone(selected.GroupIDs)
		for _, binding := range selected.AccountGroups {
			groupIDs = append(groupIDs, binding.GroupID)
		}
		slices.Sort(groupIDs)
		groupIDs = slices.Compact(groupIDs)
	}
	reader := gatewayAccountReader(s.accountRepo, nil)
	if reader == nil {
		return s.sendErrorAndEnd(c, "Gateway connection test admission is unavailable")
	}
	var route *gatewayAccountTestRoute
	eligible := 0
	for _, id := range groupIDs {
		if id <= 0 {
			return s.sendErrorAndEnd(c, ErrGatewayAccountGroup.Error())
		}
		if !ObserverCanManageGroup(ctx, id) {
			continue
		}
		current, group, err := reader.GatewayRequestAccount(ctx, selected.ID, id)
		// Storage uncertainty cannot be treated as an ineligible route: doing so
		// could turn an ambiguous request into an arbitrary first-group choice.
		if err != nil || current == nil || group == nil || group.ID != id {
			return s.sendErrorAndEnd(c, "Gateway connection test could not load the account and selected group")
		}
		if current.ID != selected.ID || !current.IsGateway() || current.Platform != PlatformGateway ||
			ValidateGatewayAccountIdentity(current.Platform, current.Type, current.Credentials) != nil || current.ParentAccountID != nil || current.AuthConnectionID != "" {
			return s.sendErrorAndEnd(c, ErrGatewayAccountIdentity.Error())
		}
		candidate, err := resolveGatewayAccountTestRoute(ctx, selected, current, group, model, opts.Platform)
		if err != nil {
			return s.sendErrorAndEnd(c, fmt.Sprintf("Gateway connection test admission failed: %s", err.Error()))
		}
		if candidate != nil {
			route = candidate
			eligible++
		}
	}
	if eligible == 0 {
		return s.sendErrorAndEnd(c, "Gateway connection test has no eligible model protocol and group; select a supported text model or explicit group_id/platform")
	}
	if eligible != 1 {
		return s.sendErrorAndEnd(c, "Gateway connection test has multiple eligible groups; select an explicit group_id")
	}
	c.Request = c.Request.WithContext(route.ctx)
	probe := &AccountTestService{accountRepo: s.accountRepo, httpUpstream: s.httpUpstream, cfg: s.cfg, tlsFPProfileService: s.tlsFPProfileService}
	switch route.family {
	case domain.PlatformGatewayOpenAI:
		return probe.testOpenAIAccountConnection(c, route.account, route.model, prompt, AccountTestModeDefault)
	case domain.PlatformGatewayAnthropic:
		return probe.testClaudeAccountConnection(c, route.account, route.model)
	case domain.PlatformGatewayGemini:
		return probe.testGeminiAccountConnection(c, route.account, route.model, prompt)
	default:
		return s.sendErrorAndEnd(c, "Selected gateway protocol is not supported by connection tests")
	}
}

type gatewayAccountTestRoute struct {
	ctx     context.Context
	account *Account
	model   string
	family  domain.PlatformGateway
}

// A nil route is a known policy/protocol exclusion, not a storage failure.
func resolveGatewayAccountTestRoute(ctx context.Context, selected, current *Account, group *Group, model, platform string) (*gatewayAccountTestRoute, error) {
	// Resolve on a copy: lazy model caches must not mutate the stored identity.
	scope := *current
	scope.Credentials = maps.Clone(current.Credentials)
	scope.modelMappingCacheReady = false
	wireModel := scope.GetMappedModel(model)
	target := strings.TrimSpace(platform)
	if target == "" {
		target, _ = ResolvedTargetPlatformFromContext(ctx)
	}
	if target == "" && group.Platform != PlatformComposite {
		target = group.Platform
	}
	detected, known := DetectModelPlatform(wireModel)
	if target == "" && known {
		target = detected
	}
	spec, concrete := domain.LookupPlatform(target)
	if !concrete || target == PlatformTypeSafe {
		return nil, nil
	}
	family := spec.Gateway
	if target == PlatformAntigravity && known {
		if detectedSpec, ok := domain.LookupPlatform(detected); ok {
			family = detectedSpec.Gateway
		}
	}
	if known {
		detectedSpec, _ := domain.LookupPlatform(detected)
		if detectedSpec.Gateway != family || detected == PlatformTypeSafe {
			return nil, nil
		}
	}
	if !gatewayAccountTestTextModel(wireModel) {
		return nil, nil
	}
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	ctx = WithResolvedTargetPlatform(ctx, target)
	view, err := gatewayDiagnosticAccountFromSnapshot(ctx, selected, current, group, family, model)
	if err != nil {
		if err == ErrGatewayAccountGroup || err == ErrGatewayProtocolsPending {
			return nil, nil
		}
		return nil, err
	}
	// Admission checked the public source key and group policy. Apply its mapping
	// exactly once, including wildcard aliases, before entering native adapters.
	view.Credentials["model_mapping"] = map[string]any{wireModel: wireModel}
	view.modelMappingCacheReady = false
	// Probes use the site's own key and proxy, never provider plugins or borrowed
	// credentials. Keep the existing explicit Responses/Chat selection only.
	view.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesMode:      current.Extra[openai_compat.ExtraKeyResponsesMode],
		openai_compat.ExtraKeyResponsesSupported: current.Extra[openai_compat.ExtraKeyResponsesSupported],
	}
	return &gatewayAccountTestRoute{ctx: ctx, account: view, model: wireModel, family: family}, nil
}

func gatewayAccountTestTextModel(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		name = name[slash+1:]
	}
	if name == "" || strings.Contains(name, "image") || strings.Contains(name, "video") || strings.Contains(name, "audio") || strings.Contains(name, "embedding") || strings.Contains(name, "moderation") {
		return false
	}
	for _, prefix := range []string{"tts-", "whisper-", "dall-e-", "grok-voice", "jev-"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}
