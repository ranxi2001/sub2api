package service

import (
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Exercise billing, including mixed cache TTLs, through all three default
// sources. Rates are USD per million tokens from the 2026-11-18 short-context
// Standard table; Pro follows the owner's explicit Astra price mapping.
func TestOpenAIOfficialShortContextPricing20261118(t *testing.T) {
	body, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	catalog := &PricingService{}
	catalog.pricingData, err = catalog.parsePricingData(body)
	require.NoError(t, err)
	for source, svc := range map[string]*BillingService{
		"builtin":      NewBillingService(&config.Config{}, nil),
		"empty_remote": NewBillingService(&config.Config{}, &PricingService{}),
		"bundled":      NewBillingService(&config.Config{}, catalog),
	} {
		for _, tc := range []struct {
			model                                   string
			input, read, output, write30m, write12h float64
		}{
			{"gpt-6.2-astra", 8, .4, 40, 10, 20},
			{"gpt-6.2-astra-pro", 8, .4, 40, 10, 20},
			{"gpt-6.2-sol", 2, .1, 10, 2.5, 5},
			{"gpt-6-astra", 10, 1, 50, 12.5, 25},
			{"gpt-6.1-sol", 2, .1, 10, 2.5, 5},
			{"gpt-6.1-terra", 1, .05, 5, 1.25, 2.5},
			{"gpt-6-sol", 2, .2, 10, 2.5, 5},
			{"gpt-6-luna", .1, .01, .5, .125, .25},
			{"gpt-5.6-sol", 4, .4, 20, 5, 10},
			{"gpt-5.6-terra", 2, .2, 12, 2.5, 5},
			{"gpt-5.6-luna", .2, .02, 1.2, .25, .5},
		} {
			t.Run(source+"/"+tc.model, func(t *testing.T) {
				// Total input stays below the short-context threshold. Cache
				// writes are split so a flat 30m price cannot pass unnoticed.
				usage := UsageTokens{InputTokens: 10000, OutputTokens: 2000,
					CacheReadTokens: 3000, CacheCreationTokens: 5000,
					CacheCreation30mTokens: 2000, CacheCreation12hTokens: 3000}
				cost, err := svc.CalculateCost(tc.model, usage, .85)
				require.NoError(t, err)
				require.InDelta(t, .01*tc.input, cost.InputCost, 1e-12)
				require.InDelta(t, .002*tc.output, cost.OutputCost, 1e-12)
				require.InDelta(t, .003*tc.read, cost.CacheReadCost, 1e-12)
				require.InDelta(t, .002*tc.write30m+.003*tc.write12h, cost.CacheCreationCost, 1e-12)
				require.InDelta(t, cost.TotalCost*.85, cost.ActualCost, 1e-12)
				require.False(t, cost.LongContextBillingApplied)
			})
		}
	}
}
