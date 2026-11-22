package service

import (
	"context"
	"fmt"
	"maps"
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
	if groupID == 0 {
		if group, _ := ctx.Value(ctxkey.Group).(*Group); group != nil {
			groupID = group.ID
		} else {
			ids := make(map[int64]struct{})
			for _, id := range selected.GroupIDs {
				ids[id] = struct{}{}
			}
			for _, binding := range selected.AccountGroups {
				ids[binding.GroupID] = struct{}{}
			}
			if len(ids) != 1 {
				return s.sendErrorAndEnd(c, "Gateway connection test requires an explicit group_id when account membership is ambiguous or missing")
			}
			for id := range ids {
				groupID = id
			}
		}
	}
	if groupID <= 0 || !ObserverCanManageGroup(ctx, groupID) {
		return s.sendErrorAndEnd(c, ErrObserverScope.Error())
	}
	reader := gatewayAccountReader(s.accountRepo, nil)
	if reader == nil {
		return s.sendErrorAndEnd(c, "Gateway connection test admission is unavailable")
	}
	current, group, err := reader.GatewayRequestAccount(ctx, selected.ID, groupID)
	if err != nil || current == nil || group == nil || group.ID != groupID {
		return s.sendErrorAndEnd(c, "Gateway connection test could not load the account and selected group")
	}
	// Resolve on a copy: lazy model caches must not mutate the stored identity.
	scope := *current
	scope.Credentials = maps.Clone(current.Credentials)
	scope.modelMappingCacheReady = false
	wireModel := scope.GetMappedModel(model)
	target := strings.TrimSpace(opts.Platform)
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
		return s.sendErrorAndEnd(c, "Gateway connection test requires an unambiguous model protocol or explicit concrete platform")
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
			return s.sendErrorAndEnd(c, "Selected model does not support the selected gateway text protocol")
		}
	}
	if !gatewayAccountTestTextModel(wireModel) {
		return s.sendErrorAndEnd(c, "Selected model is not supported by gateway text connection tests")
	}
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	ctx = WithResolvedTargetPlatform(ctx, target)
	view, err := gatewayProtocolAccountFromSnapshot(ctx, selected, current, group, family, model)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Gateway connection test admission failed: %s", err.Error()))
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
	c.Request = c.Request.WithContext(ctx)
	probe := &AccountTestService{accountRepo: s.accountRepo, httpUpstream: s.httpUpstream, cfg: s.cfg, tlsFPProfileService: s.tlsFPProfileService}
	switch family {
	case domain.PlatformGatewayOpenAI:
		return probe.testOpenAIAccountConnection(c, view, wireModel, prompt, AccountTestModeDefault)
	case domain.PlatformGatewayAnthropic:
		return probe.testClaudeAccountConnection(c, view, wireModel)
	case domain.PlatformGatewayGemini:
		return probe.testGeminiAccountConnection(c, view, wireModel, prompt)
	default:
		return s.sendErrorAndEnd(c, "Selected gateway protocol is not supported by connection tests")
	}
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
