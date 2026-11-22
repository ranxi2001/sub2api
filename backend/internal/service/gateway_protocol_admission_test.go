//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type gatewayProtocolAdmissionRepo struct {
	AccountRepository
	account *Account
	group   *Group
	calls   int
}

func (r *gatewayProtocolAdmissionRepo) GatewayRequestAccount(_ context.Context, _, _ int64) (*Account, *Group, error) {
	r.calls++
	return r.account, r.group, nil
}

func gatewayProtocolFixture(target string) (*gatewayProtocolAdmissionRepo, context.Context) {
	a := &Account{ID: 959, Platform: PlatformGateway, Type: AccountTypeGateway, Status: StatusActive, Schedulable: true,
		Concurrency: 2, Priority: 7, GroupIDs: []int64{1}, AccountGroups: []AccountGroup{{GroupID: 1}},
		Credentials: map[string]any{"upstream_kind": "new_api", "base_url": "https://gateway.example/tenant/v1", "api_key": "synthetic-only"}}
	g := &Group{ID: 1, Platform: target, Status: StatusActive, Hydrated: true}
	return &gatewayProtocolAdmissionRepo{account: a, group: g}, context.WithValue(context.Background(), ctxkey.Group, g)
}

func TestGatewayProtocolAdmissionUntouchedModelMappingRemainsUnrestricted(t *testing.T) {
	for _, kind := range []string{"new_api", "sub2api"} {
		for _, scenario := range []struct {
			platform string
			family   domain.PlatformGateway
			model    string
		}{
			{PlatformOpenAI, domain.PlatformGatewayOpenAI, "gpt-6.1-sol"},
			{PlatformGemini, domain.PlatformGatewayGemini, "gemini-3.1-pro"},
		} {
			t.Run(kind+"/"+scenario.platform, func(t *testing.T) {
				repo, ctx := gatewayProtocolFixture(scenario.platform)
				repo.account.Credentials["upstream_kind"] = kind
				// The untouched create form saves an empty mapping: no provider's
				// built-in whitelist may narrow this explicitly bound gateway.
				repo.account.Credentials["model_mapping"] = map[string]any{}
				view, err := prepareGatewayProtocolAccount(ctx, repo.account, repo, scenario.family, scenario.model)
				require.NoError(t, err)
				require.Equal(t, scenario.model, view.GetMappedModel(scenario.model))
				// Explicit administrator restrictions still govern the same model.
				repo.account.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4": "claude-sonnet-4"}
				_, err = prepareGatewayProtocolAccount(ctx, repo.account, repo, scenario.family, scenario.model)
				require.ErrorIs(t, err, ErrGatewayAccountGroup)
			})
		}
	}
}

func TestGatewayProtocolAdmissionAllFamiliesKeepOneIdentity(t *testing.T) {
	for _, spec := range domain.Platforms() {
		for _, composite := range []bool{false, true} {
			t.Run(spec.ID+map[bool]string{false: "/concrete", true: "/composite"}[composite], func(t *testing.T) {
				repo, ctx := gatewayProtocolFixture(spec.ID)
				if composite {
					repo.group.Platform = PlatformComposite
					ctx = WithResolvedTargetPlatform(ctx, spec.ID)
				}
				family := spec.Gateway
				if family == domain.PlatformGatewayAntigravity {
					family = domain.PlatformGatewayAnthropic
				}
				view, err := prepareGatewayProtocolAccount(ctx, repo.account, repo, family, "public-model")
				require.NoError(t, err)
				require.Equal(t, repo.account.ID, view.ID)
				require.Equal(t, 2, view.Concurrency)
				require.Equal(t, 7, view.Priority)
				require.Equal(t, AccountTypeAPIKey, view.Type)
				require.Equal(t, "synthetic-only", view.GetCredential("api_key"))
				wantBase := "https://gateway.example/tenant/v1"
				if family == domain.PlatformGatewayGemini || family == domain.PlatformGatewayAnthropic {
					wantBase = "https://gateway.example/tenant"
				}
				require.Equal(t, wantBase, view.GetCredential("base_url"))
				require.Equal(t, "public-model", view.GetMappedModel("public-model"), "default model names pass through")
				require.Equal(t, PlatformGateway, repo.account.Platform)
				require.Equal(t, AccountTypeGateway, repo.account.Type)
				view.Credentials["api_key"] = "changed-view-only"
				require.Equal(t, "synthetic-only", repo.account.GetCredential("api_key"))
			})
		}
	}
}

func TestGatewayProtocolAdmissionRejectsUnboundWrongTargetAndOAuth(t *testing.T) {
	for _, scenario := range []string{"no-context", "unbound", "wrong-target", "oauth-only", "disabled-group", "disabled-account", "unschedulable", "unresolved-composite", "invalid-url", "model-group", "model-account", "wrong-protocol", "legacy-replacement"} {
		t.Run(scenario, func(t *testing.T) {
			repo, ctx := gatewayProtocolFixture(PlatformOpenAI)
			family := domain.PlatformGatewayOpenAI
			switch scenario {
			case "no-context":
				ctx = context.Background()
			case "unbound":
				repo.account.GroupIDs, repo.account.AccountGroups = nil, nil
			case "wrong-target":
				ctx = WithResolvedTargetPlatform(ctx, PlatformKimi)
			case "oauth-only":
				repo.group.RequireOAuthOnly = true
			case "disabled-group":
				repo.group.Status = StatusDisabled
			case "disabled-account":
				repo.account.Status = StatusDisabled
			case "unschedulable":
				repo.account.Schedulable = false
			case "unresolved-composite":
				repo.group.Platform = PlatformComposite
			case "invalid-url":
				repo.account.Credentials["base_url"] = "https://gateway.example?key=secret"
			case "model-group":
				repo.account.AccountGroups[0].AllowedModels = []string{"other"}
			case "model-account":
				repo.account.Credentials["model_mapping"] = map[string]any{"other": "other"}
			case "wrong-protocol":
				family = domain.PlatformGatewayGemini
			case "legacy-replacement":
				repo.account.Type, repo.account.Platform = AccountTypeAPIKey, PlatformOpenAI
			}
			_, err := prepareGatewayProtocolAccount(ctx, repo.account, repo, family, "public-model")
			require.Error(t, err)
		})
	}
}

func TestGatewayProtocolAdmissionBaseVersionAndModelScopes(t *testing.T) {
	for _, suffix := range []string{"", "/v1", "/v1beta", "/v1/"} {
		repo, ctx := gatewayProtocolFixture(PlatformGemini)
		repo.account.Credentials["base_url"] = "https://gateway.example/tenant" + suffix
		view, err := prepareGatewayProtocolAccount(ctx, repo.account, repo, domain.PlatformGatewayGemini, "public-model")
		require.NoError(t, err)
		require.Equal(t, "https://gateway.example/tenant", view.GetCredential("base_url"))
		require.Equal(t, "https://gateway.example/tenant"+suffix, repo.account.GetCredential("base_url"), "URL normalization is request-local")
	}
	repo, ctx := gatewayProtocolFixture(PlatformComposite)
	ctx = WithCompositeRouteDecision(ctx, CompositeRouteDecision{Matched: true, PublicModel: "public-alias", UpstreamModel: "resolved-model", TargetPlatform: PlatformOpenAI})
	repo.group.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"public-alias"}}
	repo.account.AccountGroups[0].AllowedModels = []string{"resolved-model"}
	repo.account.Credentials["model_mapping"] = map[string]any{"resolved-model": "upstream-model"}
	view, err := prepareGatewayProtocolAccount(ctx, repo.account, repo, domain.PlatformGatewayOpenAI, "resolved-model")
	require.NoError(t, err)
	require.Equal(t, "upstream-model", view.GetMappedModel("resolved-model"))
	repo.account.Credentials["api_base_urls"] = map[string]any{"responses": "https://alternate.invalid"}
	repo.account.Extra = map[string]any{"openai_copilot_sdk": true, "openai_connections_managed": true}
	view, err = prepareGatewayProtocolAccount(ctx, repo.account, repo, domain.PlatformGatewayOpenAI, "resolved-model")
	require.NoError(t, err)
	require.NotContains(t, view.Credentials, "api_base_urls")
	require.False(t, view.IsCopilotSDKEnabled())
	require.False(t, view.HasManagedOpenAIConnections())
}

func TestGatewayProtocolAdmissionAntigravityAcceptsBothNativeProtocols(t *testing.T) {
	repo, ctx := gatewayProtocolFixture(PlatformAntigravity)
	for _, family := range []domain.PlatformGateway{domain.PlatformGatewayAnthropic, domain.PlatformGatewayGemini} {
		view, err := prepareGatewayProtocolAccount(ctx, repo.account, repo, family, "custom-model")
		require.NoError(t, err)
		require.Equal(t, "custom-model", view.GetMappedModel("custom-model"), "do not inject official Antigravity mappings into a relay")
	}
	ctx = context.WithValue(ctx, ctxkey.ForcePlatform, PlatformGemini)
	_, err := prepareGatewayProtocolAccount(ctx, repo.account, repo, domain.PlatformGatewayGemini, "custom-model")
	require.Error(t, err, "forced URL must not bypass concrete group target")
}

func TestGatewayProtocolAdmissionSharedStateAndSelectionIdentity(t *testing.T) {
	repo, ctx := gatewayProtocolFixture(PlatformComposite)
	selected := *repo.account
	selected.Credentials = map[string]any{"upstream_kind": "new_api", "base_url": "https://gateway.example/tenant/v1", "api_key": "synthetic-only"}
	repo.account.Credentials["api_key"] = "rotated-after-selection"
	_, err := prepareGatewayProtocolAccount(WithResolvedTargetPlatform(ctx, PlatformOpenAI), &selected, repo, domain.PlatformGatewayOpenAI, "public-model")
	require.ErrorIs(t, err, ErrGatewayAccountIdentity)
	repo.account.Credentials["api_key"] = "synthetic-only"
	repo.account.Status = StatusError
	for _, target := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformAntigravity} {
		family := domain.PlatformGateway(target)
		if target == PlatformAntigravity {
			family = domain.PlatformGatewayAnthropic
		}
		_, err := prepareGatewayProtocolAccount(WithResolvedTargetPlatform(ctx, target), &selected, repo, family, "public-model")
		require.Error(t, err, "one shared account error blocks all target protocols")
	}
}

func TestGatewayProtocolAdmissionPreservesModelCooldown(t *testing.T) {
	repo, ctx := gatewayProtocolFixture(PlatformOpenAI)
	repo.account.Extra = map[string]any{modelRateLimitsKey: map[string]any{
		"public-model": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
	}}
	_, err := prepareGatewayProtocolAccount(ctx, repo.account, repo, domain.PlatformGatewayOpenAI, "public-model")
	require.ErrorIs(t, err, ErrGatewayProtocolsPending, "transport view must retain existing per-model cooldown admission")
	_, err = prepareGatewayProtocolAccount(ctx, repo.account, repo, domain.PlatformGatewayOpenAI, "another-model")
	require.NoError(t, err, "a model cooldown does not erase other model capacity")
}
