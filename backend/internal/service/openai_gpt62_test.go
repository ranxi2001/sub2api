package service

import (
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestGPT62ModelIdentityAndReasoning(t *testing.T) {
	for _, model := range []string{"gpt-6.2-astra", "gpt-6.2-astra-pro", "gpt-6.2-sol"} {
		for _, suffix := range []string{"", "-max", "-2026-11-16", "-openai-compact"} {
			alias := "openai/" + model + suffix
			require.Equal(t, model, normalizeOpenAIGPT62Model(alias))
			require.Equal(t, model, normalizeCodexModel(alias))
			require.Equal(t, "max", normalizeOpenAIReasoningEffortForModel("max", alias))
		}
		require.Contains(t, openai.DefaultModelIDs(), model)
		descriptor := newConfiguredCodexModelDescriptor(model)
		require.Equal(t, model, descriptor.Slug)
		var levels []string
		for _, level := range descriptor.SupportedReasoningLevels {
			levels = append(levels, level.Effort)
		}
		require.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, levels)
	}
	for _, unknown := range []string{"gpt-6.2-astral", "gpt-6.2-astra-prototype", "gpt-6.2-astra-pro-preview", "gpt-6.2-solitude", "gpt-6.20-astra"} {
		require.Empty(t, normalizeOpenAIGPT62Model(unknown), unknown)
	}
}

func TestGPT62DefaultPricingAndCacheTTL(t *testing.T) {
	data, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	catalog := &PricingService{}
	catalog.pricingData, err = catalog.parsePricingData(data)
	require.NoError(t, err)
	for source, svc := range map[string]*BillingService{
		"builtin":      NewBillingService(&config.Config{}, nil),
		"empty_remote": NewBillingService(&config.Config{}, &PricingService{}),
		"bundled":      NewBillingService(&config.Config{}, catalog),
	} {
		for _, tc := range []struct {
			model                      string
			input, output, write, read float64
		}{
			{"gpt-6.2-astra", 10e-6, 50e-6, 12.5e-6, 1e-6},
			{"gpt-6.2-astra-pro", 10e-6, 50e-6, 12.5e-6, 1e-6},
			{"gpt-6.2-sol", 2e-6, 10e-6, 2.5e-6, 0.1e-6},
		} {
			t.Run(source+"/"+tc.model, func(t *testing.T) {
				for _, model := range []string{tc.model, "openai/" + tc.model + "-max"} {
					price, err := svc.GetModelPricing(model)
					require.NoError(t, err)
					require.NotNil(t, price.CacheCreation12hPricePerToken)
					require.InDelta(t, tc.write, *price.CacheCreation12hPricePerToken, 1e-12)
					// The supplied cards have no context ranges. A long prompt must
					// not silently inherit an older model's context surcharge.
					tokens := UsageTokens{InputTokens: 300000, OutputTokens: 500, CacheReadTokens: 2000, CacheCreationTokens: 1000, CacheCreation30mTokens: 400, CacheCreation12hTokens: 600}
					for tier, multiplier := range map[string]float64{"": 1, "priority": 2, "flex": 0.5, "ultrafast": 6} {
						cost, err := svc.CalculateCostWithServiceTier(model, tokens, 1, tier)
						require.NoError(t, err)
						require.InDelta(t, 300000*tc.input*multiplier, cost.InputCost, 1e-10)
						require.InDelta(t, 500*tc.output*multiplier, cost.OutputCost, 1e-10)
						require.InDelta(t, 1000*tc.write*multiplier, cost.CacheCreationCost, 1e-10)
						require.InDelta(t, 2000*tc.read*multiplier, cost.CacheReadCost, 1e-10)
						require.False(t, cost.LongContextBillingApplied)
					}
				}
			})
		}
	}
}

func TestGPT62PricingPreservesIndependentOverrides(t *testing.T) {
	catalog := &PricingService{}
	var err error
	catalog.pricingData, err = catalog.parsePricingData([]byte(`{"gpt-6.2-astra":{"input_cost_per_token":0.000003,"output_cost_per_token":0.000004},"gpt-6.2-astra-pro":{"input_cost_per_token":0.000007,"output_cost_per_token":0.000008,"cache_creation_input_token_cost":0,"cache_creation_input_token_cost_12h":0}}`))
	require.NoError(t, err)
	svc := NewBillingService(&config.Config{}, catalog)
	pro, err := svc.GetModelPricing("openai/gpt-6.2-astra-pro-max")
	require.NoError(t, err)
	require.InDelta(t, 7e-6, pro.InputPricePerToken, 1e-12)
	require.InDelta(t, 8e-6, pro.OutputPricePerToken, 1e-12)
	require.Zero(t, pro.CacheCreationPricePerToken)
	require.NotNil(t, pro.CacheCreation12hPricePerToken)
	require.Zero(t, *pro.CacheCreation12hPricePerToken)
	sol, err := svc.GetModelPricing("gpt-6.2-sol")
	require.NoError(t, err)
	require.InDelta(t, 2e-6, sol.InputPricePerToken, 1e-12)
	zero := 0.0
	custom, err := svc.GetModelPricingWithChannel("gpt-6.2-sol", &ChannelModelPricing{InputPrice: &zero, OutputPrice: &zero, CacheReadPrice: &zero, CacheWritePrice: &zero, CacheWrite12hPrice: &zero})
	require.NoError(t, err)
	require.Zero(t, custom.InputPricePerToken)
	require.Zero(t, custom.OutputPricePerToken)
	require.Zero(t, custom.CacheReadPricePerToken)
	require.Zero(t, custom.CacheCreationPricePerToken)
	require.Zero(t, *custom.CacheCreation12hPricePerToken)
}
