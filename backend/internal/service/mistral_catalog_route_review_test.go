//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Match the repository's PlatformIn filtering rather than returning all fixture
// accounts (which hid the production whitelist omission).
type mistralCatalogFilteringRepo struct {
	AccountRepository
	accounts []Account
}

func (r mistralCatalogFilteringRepo) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	return r.accounts, nil
}
func (r mistralCatalogFilteringRepo) ListModelAvailabilityCandidates(_ context.Context, _ *int64, platforms []string, _ bool) ([]Account, error) {
	var out []Account
	for _, a := range r.accounts {
		for _, p := range platforms {
			if a.Platform == p {
				out = append(out, a)
				break
			}
		}
	}
	return out, nil
}
func TestMistralReviewCatalogService(t *testing.T) {
	for _, platform := range []string{PlatformMistral, PlatformOpenAI, PlatformDeepseek, PlatformOpenCodeGo} {
		for _, groupPlatform := range []string{platform, PlatformComposite} {
			t.Run(platform+"/"+groupPlatform, func(t *testing.T) {
				const model = "mistral-review-fixture"
				reasoning := true
				a := Account{ID: 99, Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{model: model}}}
				a.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Source: "offline fixture", Models: map[string]UpstreamModelMetadata{model: {ID: model, Reasoning: &reasoning, ContextWindow: 456000, MaxContextWindow: 789000, InputModalities: []string{"text", "image"}, SupportedReasoningLevels: []string{"low", "high", "max"}, DefaultReasoningLevel: "high"}}})
				svc := &GatewayService{accountRepo: mistralCatalogFilteringRepo{accounts: []Account{a}}}
				body, err := svc.BuildCodexModelsManifestForGroup(context.Background(), &Group{ID: 7, Platform: groupPlatform}, "", []string{model})
				require.NoError(t, err)
				models := decodeCodexManifestModels(t, body)
				require.Len(t, models, 1)
				require.Equal(t, float64(456000), models[0]["context_window"])
				require.Equal(t, float64(789000), models[0]["max_context_window"])
				require.Equal(t, []string{"low", "high", "max"}, effortsFromManifestModel(t, models[0]))
				require.Equal(t, "high", models[0]["default_reasoning_level"])
				require.Equal(t, []any{"text", "image"}, models[0]["input_modalities"])
				require.True(t, groupCodexModelSupportsImageInput(groupPlatform, model, []Account{a}, nil, true))
				require.True(t, accountCodexModelSupportsImageInput(&a, model))
				if platform == PlatformMistral {
					require.False(t, accountCodexModelSupportsImageInput(&a, "deepseek-v4-flash-vision-exp"), "Mistral needs its own synced capabilities")
					require.False(t, accountCodexModelSupportsImageInput(&a, "pixtral-unknown"))
				}
				// A candidate without image capability must narrow the group's contract.
				a.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{model: {ID: model, InputModalities: []string{"text"}}}})
				require.False(t, groupCodexModelSupportsImageInput(groupPlatform, model, []Account{a}, nil, true))
			})
		}
	}
}

func TestMistralReviewCompositeResolver(t *testing.T) {
	ctx := context.Background()
	models := []string{"mistral/any-model", "mixtral-8x7b-latest", "open-mistral-7b", "open-mixtral-8x22b", "mathstral-7b-latest", "mistral/zai-glm-5-3", "mistral-small-latest", "codestral-latest", "pixtral-12b-latest", "magistral-small-latest", "ministral-8b-latest", "devstral-small-latest", "voxtral-small-latest"}
	for _, model := range models {
		for _, mapping := range []map[string]any{nil, {"*": "mistral-small-latest"}} {
			t.Run(model, func(t *testing.T) {
				a := Account{ID: 1, Platform: PlatformMistral, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": mapping}}
				svc := &GatewayService{accountRepo: &compositeOwnershipAccountRepo{accounts: []Account{a}}}
				resolver := NewCompositeRouteResolver(nil)
				resolver.SetModelOwnershipResolver(svc.resolveCompositeModelOwnership)
				decision, err := resolver.Resolve(ctx, 7, model, CompositeRouteEndpointMessages)
				require.NoError(t, err)
				require.True(t, decision.Matched)
				require.Equal(t, PlatformMistral, decision.TargetPlatform)
				require.Equal(t, CompositeRouteSourceDetector, decision.Source)
			})
		}
	}
	// Shared GLM names retain their prior provider or stay unresolved without evidence.
	for _, model := range []string{"glm-5.3", "zai-glm-5-3"} {
		resolver := NewCompositeRouteResolver(nil)
		d, err := resolver.Resolve(ctx, 7, model, CompositeRouteEndpointMessages)
		require.NoError(t, err)
		if model == "glm-5.3" {
			require.Equal(t, PlatformZhipu, d.TargetPlatform)
		} else {
			require.False(t, d.Matched)
		}
	}
	model := "mistral-small-latest"
	accounts := []Account{{ID: 1, Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{model: "custom-model"}}}}
	svc := &GatewayService{accountRepo: &compositeOwnershipAccountRepo{accounts: accounts}}
	resolver := NewCompositeRouteResolver(nil)
	resolver.SetModelOwnershipResolver(svc.resolveCompositeModelOwnership)
	d, err := resolver.Resolve(ctx, 7, model, CompositeRouteEndpointMessages)
	require.NoError(t, err)
	require.Equal(t, PlatformOpenAI, d.TargetPlatform)
	require.Equal(t, CompositeRouteSourceAccount, d.Source)
	accounts = append(accounts, Account{ID: 2, Platform: PlatformMistral, Credentials: map[string]any{"model_mapping": map[string]any{model: model}}})
	svc.accountRepo = &compositeOwnershipAccountRepo{accounts: accounts}
	d, err = resolver.Resolve(ctx, 7, model, CompositeRouteEndpointMessages)
	require.NoError(t, err)
	require.False(t, d.Matched)
	require.Equal(t, CompositeRouteReasonAmbiguous, d.Reason)
	resolver.repo = compositeRouteRepoStub{routes: []CompositeModelRoute{{ID: 1, GroupID: 7, PublicModel: model, MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformMistral, UpstreamModel: model, Endpoint: CompositeRouteEndpointAny, Enabled: true}}}
	d, err = resolver.Resolve(ctx, 7, model, CompositeRouteEndpointMessages)
	require.NoError(t, err)
	require.True(t, d.Matched)
	require.Equal(t, PlatformMistral, d.TargetPlatform)
	require.Equal(t, CompositeRouteSourceExplicit, d.Source)
	// An exact account mapping is sufficient evidence even for a shared GLM name.
	svc.accountRepo = &compositeOwnershipAccountRepo{accounts: []Account{{Platform: PlatformMistral, Credentials: map[string]any{"model_mapping": map[string]any{"zai-glm-5-3": "zai-glm-5-3"}}}}}
	d, err = resolver.Resolve(ctx, 7, "zai-glm-5-3", CompositeRouteEndpointMessages)
	require.NoError(t, err)
	require.True(t, d.Matched)
	require.Equal(t, PlatformMistral, d.TargetPlatform)
}
