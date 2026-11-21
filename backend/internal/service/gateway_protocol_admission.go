package service

import (
	"context"
	"encoding/json"
	"maps"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
)

func gatewayProtocolParsedModel(parsed *ParsedRequest) string {
	if parsed == nil {
		return ""
	}
	return parsed.Model
}

func gatewayProtocolBodyModel(body []byte) string {
	var request struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &request)
	return request.Model
}

func (s *GatewayService) gatewayProtocolAccount(ctx context.Context, account *Account, family domain.PlatformGateway, model string) (*Account, error) {
	if s == nil {
		return nil, ErrGatewayProtocolsPending
	}
	return prepareGatewayProtocolAccount(ctx, account, gatewayAccountReader(s.accountRepo, s.schedulerSnapshot), family, model)
}

func (s *OpenAIGatewayService) gatewayProtocolAccount(ctx context.Context, account *Account, family domain.PlatformGateway, model string) (*Account, error) {
	if s == nil {
		return nil, ErrGatewayProtocolsPending
	}
	return prepareGatewayProtocolAccount(ctx, account, gatewayAccountReader(s.accountRepo, s.schedulerSnapshot), family, model)
}

func (s *GeminiMessagesCompatService) gatewayProtocolAccount(ctx context.Context, account *Account, family domain.PlatformGateway, model string) (*Account, error) {
	if s == nil {
		return nil, ErrGatewayProtocolsPending
	}
	return prepareGatewayProtocolAccount(ctx, account, gatewayAccountReader(s.accountRepo, s.schedulerSnapshot), family, model)
}

// Relay accounts on Antigravity URLs use native protocols, never the official
// cloudcode OAuth envelope or its default model aliases.
func (s *AntigravityGatewayService) forwardGatewayMessages(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	if s == nil {
		return nil, ErrGatewayProtocolsPending
	}
	cfg := &config.Config{}
	if s.settingService != nil && s.settingService.cfg != nil {
		cfg = s.settingService.cfg
	}
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	if err != nil {
		return nil, err
	}
	relay := &GatewayService{cfg: cfg, httpUpstream: s.httpUpstream, accountRepo: s.accountRepo, schedulerSnapshot: s.schedulerSnapshot,
		rateLimitService: s.rateLimitService, settingService: s.settingService,
		responseHeaderFilter: compileResponseHeaderFilter(cfg), deferredService: &DeferredService{}}
	return relay.Forward(ctx, c, account, parsed)
}

func (s *AntigravityGatewayService) forwardGatewayGemini(ctx context.Context, c *gin.Context, account *Account, model, action string, stream bool, body []byte) (*ForwardResult, error) {
	if s == nil {
		return nil, ErrGatewayProtocolsPending
	}
	cfg := &config.Config{}
	if s.settingService != nil && s.settingService.cfg != nil {
		cfg = s.settingService.cfg
	}
	relay := &GeminiMessagesCompatService{cfg: cfg, httpUpstream: s.httpUpstream, accountRepo: s.accountRepo, schedulerSnapshot: s.schedulerSnapshot,
		rateLimitService: s.rateLimitService}
	return relay.ForwardNative(ctx, c, account, model, action, stream, body)
}

// GatewayRequestAccountReader reloads the account and its requested group at
// final admission. It never infers membership from an account platform or URL.
type GatewayRequestAccountReader interface {
	GatewayRequestAccount(ctx context.Context, accountID, groupID int64) (*Account, *Group, error)
}

func gatewayAccountReader(repo AccountRepository, snapshot *SchedulerSnapshotService) GatewayRequestAccountReader {
	if snapshot != nil && snapshot.localSnapshotReader != nil {
		reader, _ := snapshot.localSnapshotReader.(GatewayRequestAccountReader)
		return reader
	}
	reader, _ := repo.(GatewayRequestAccountReader)
	return reader
}

// prepareGatewayProtocolAccount makes an isolated API-key transport view. The
// caller retains the original gateway for usage/procurement bookkeeping. ID,
// concurrency, proxy and runtime state remain those of the one shared account.
func prepareGatewayProtocolAccount(ctx context.Context, selected *Account, reader GatewayRequestAccountReader, family domain.PlatformGateway, model string) (*Account, error) {
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if selected == nil || !selected.IsGateway() || group == nil || group.ID <= 0 {
		return nil, ErrGatewayProtocolsPending
	}
	if model == "" {
		return nil, ErrGatewayProtocolsPending
	}
	if reader == nil {
		return nil, ErrGatewayProtocolsPending
	}
	current, liveGroup, err := reader.GatewayRequestAccount(ctx, selected.ID, group.ID)
	if err != nil {
		return nil, err
	}
	return gatewayProtocolAccountFromSnapshot(ctx, selected, current, liveGroup, family, model)
}

func gatewayProtocolAccountFromSnapshot(ctx context.Context, selected, current *Account, liveGroup *Group, family domain.PlatformGateway, model string) (*Account, error) {
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if selected == nil || group == nil || model == "" {
		return nil, ErrGatewayProtocolsPending
	}
	if current == nil || !current.IsGateway() || current.Platform != PlatformGateway || current.ID != selected.ID || liveGroup == nil || liveGroup.ID != group.ID ||
		!liveGroup.IsActive() || ValidateGatewayAccountGroup(current, liveGroup) != nil {
		return nil, ErrGatewayAccountGroup
	}
	if err := ValidateGatewayAccountIdentity(current.Platform, current.Type, current.Credentials); err != nil {
		return nil, err
	}
	if current.ParentAccountID != nil || current.AuthConnectionID != "" {
		return nil, ErrGatewayAccountIdentity
	}
	// A credential/proxy edit after selection requires a fresh selection, rather
	// than forwarding with one identity and logging another identity's snapshot.
	for _, key := range []string{"api_key", "base_url", GatewayUpstreamKindCredentialKey} {
		if selected.GetCredential(key) != current.GetCredential(key) {
			return nil, ErrGatewayAccountIdentity
		}
	}
	if (selected.ProxyID == nil) != (current.ProxyID == nil) ||
		(selected.ProxyID != nil && *selected.ProxyID != *current.ProxyID) {
		return nil, ErrGatewayAccountIdentity
	}
	member := false
	for _, id := range current.GroupIDs {
		member = member || id == liveGroup.ID
	}
	for _, binding := range current.AccountGroups {
		member = member || binding.GroupID == liveGroup.ID
	}
	if !member {
		return nil, ErrGatewayAccountGroup
	}
	target := liveGroup.Platform
	if resolved, ok := ResolvedTargetPlatformFromContext(ctx); ok {
		target = resolved
	}
	if forced, _ := ctx.Value(ctxkey.ForcePlatform).(string); forced != "" {
		if resolved, ok := ResolvedTargetPlatformFromContext(ctx); ok && resolved != forced {
			return nil, ErrGatewayAccountGroup
		}
		target = forced
	}
	spec, concrete := domain.LookupPlatform(target)
	if !concrete || (liveGroup.Platform != PlatformComposite && liveGroup.Platform != target) {
		return nil, ErrGatewayAccountGroup
	}
	nativeAntigravity := target == PlatformAntigravity && (family == domain.PlatformGatewayAnthropic || family == domain.PlatformGatewayGemini)
	if spec.Gateway != family && !nativeAntigravity {
		return nil, ErrGatewayProtocolsPending
	}
	// Validate model scope while the account still has its neutral identity: no
	// official provider defaults or passthrough options can bypass a whitelist.
	scope := *current
	scope.Credentials = maps.Clone(current.Credentials)
	scope.modelMappingCacheReady = false
	id := liveGroup.ID
	selectionModel := model
	publicModel := model
	if selection, enabled := requestGatewaySelection(ctx, &id, target); enabled && selection.family == family {
		selectionModel = selection.model
		publicModel = selection.publicModel
		if scheduling, _ := ctx.Value(gatewaySchedulingModelContextKey{}).(string); scheduling != "" {
			selectionModel = scheduling
		}
	}
	if original, ok := RequestedPublicModelFromContext(ctx); ok {
		publicModel = original
	}
	// Account source-key scope and binding eligibility follow selection, before
	// channel rewriting. Forward retains model as the channel input and applies
	// the existing account mapping to it; a wire alias grants no binding rights.
	if !liveGroup.ModelAllowlist.Allows(publicModel) || !scope.isModelMappingSupported(selectionModel) || !scope.IsModelAllowedInGroup(&id, selectionModel) {
		return nil, ErrGatewayAccountGroup
	}
	view := scope
	view.gatewayProtocolSource = &scope
	view.gatewayProtocolModel = model
	view.gatewayBindingModel = selectionModel
	view.Type = AccountTypeAPIKey
	view.Platform = string(family)
	// Provider-specific alternate credentials/endpoints are not gateway inputs.
	// Keep only the one upstream key/site and portable mapping/header settings.
	view.Credentials = make(map[string]any)
	for _, key := range []string{"api_key", "base_url", "model_mapping", "compact_model_mapping", credKeyHeaderOverrideEnabled, credKeyHeaderOverrides} {
		if value, ok := scope.Credentials[key]; ok {
			view.Credentials[key] = value
		}
	}
	// Only normalize a terminal protocol version. Tenant prefixes stay intact;
	// native builders append the protocol's own /v1 or /v1beta exactly once.
	base := strings.TrimSuffix(strings.TrimRight(view.GetCredential("base_url"), "/"), "/v1beta")
	if family == domain.PlatformGatewayGemini || family == domain.PlatformGatewayAnthropic {
		base = strings.TrimSuffix(base, "/v1")
	}
	view.Credentials["base_url"] = base
	delete(view.Credentials, "api_base_urls")
	view.Extra = maps.Clone(current.Extra)
	if view.Extra == nil {
		view.Extra = make(map[string]any)
	}
	for _, key := range []string{"openai_connections_managed", "openai_copilot_sdk", "openai_prism_browser", "custom_base_url_enabled"} {
		delete(view.Extra, key)
	}
	view.modelMappingCacheReady = false
	if !view.IsSchedulableForModelWithContext(ctx, selectionModel) || !view.IsSchedulableForModelWithContext(ctx, model) {
		return nil, ErrGatewayProtocolsPending
	}
	if family == domain.PlatformGatewayAnthropic {
		view.Extra["anthropic_passthrough"] = true
	}
	if family == domain.PlatformGatewayOpenAI {
		view.Extra["openai_passthrough"] = true
		view.Extra["openai_passthrough_model_filter"] = true
	}
	return &view, nil
}
