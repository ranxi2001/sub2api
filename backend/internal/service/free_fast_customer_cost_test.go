package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// JSON keeps this business regression compilable on the production baseline.
func TestFreeFastCustomerCostSnapshot(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	gid := int64(77)
	tier := "priority"
	inputPrice, outputPrice, cacheWrite, cacheRead := 10e-6, 50e-6, 12.5e-6, 1e-6
	key := &APIKey{ID: 1020, GroupID: &gid, Group: &Group{
		ID: gid, Platform: PlatformOpenAI, Hydrated: true, RateMultiplier: 0.6, FreeOpenAIFast: true,
		ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-5.4"}, BillingMode: BillingModeToken,
			InputPrice: &inputPrice, OutputPrice: &outputPrice, CacheWritePrice: &cacheWrite, CacheReadPrice: &cacheRead}},
	}}
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{RequestID: "synthetic-customer-cost", Model: "gpt-5.4", ServiceTier: &tier,
			Usage: OpenAIUsage{InputTokens: 71, OutputTokens: 19}, Duration: time.Second},
		APIKey: key, User: &User{ID: 2020}, Account: &Account{ID: 3020, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.InDelta(t, 0.00332, usageRepo.lastLog.TotalCost, 1e-10)
	require.InDelta(t, 0.000996, usageRepo.lastLog.ActualCost, 1e-10)
	raw, err := json.Marshal(usageRepo.lastLog)
	require.NoError(t, err)
	var saved map[string]any
	require.NoError(t, json.Unmarshal(raw, &saved))
	detail, ok := saved["CustomerCostDetails"].(map[string]any)
	require.True(t, ok, "Free Fast must persist the Standard customer cost snapshot")
	require.Equal(t, true, detail["free_fast"])
	require.InDelta(t, 0.00071, detail["input_cost"], 1e-12)
	require.InDelta(t, 0.00095, detail["output_cost"], 1e-12)
	require.InDelta(t, 0.00166, detail["total_cost"], 1e-12)
	require.InDelta(t, 0.6, detail["multiplier"], 1e-12)
}

func TestFreeFastCustomerCostVariants(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		accountType                             string
		input, output, write30m, write12h, read int
		personal, model, reasoning              float64
		long                                    bool
		contextDisabled                         bool
	}{
		{name: "exact screenshot", input: 8345, output: 136, write30m: 1197, read: 37469},
		{name: "mixed cache TTL", input: 71, output: 19, write30m: 11, write12h: 13, read: 17},
		{name: "personal model reasoning", input: 71, output: 19, write30m: 11, write12h: 13, read: 17, personal: .3, model: 3, reasoning: 1.7},
		{name: "whole request threshold", input: 272000, output: 19},
		{name: "whole request above threshold", input: 272001, output: 19, long: true},
		{name: "cache crosses context tier", input: 271990, output: 19, write30m: 5, write12h: 6, read: 7, long: true},
		{name: "context gate disabled above threshold", input: 272001, output: 19, contextDisabled: true},
		{name: "subscription exemption above 500k", input: 500001, output: 19, accountType: AccountTypeOAuth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := tc.accountType
			if kind == "" {
				kind = AccountTypeAPIKey
			}
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			rates := &openAIUserGroupRateRepoStub{}
			rate := .5
			if tc.personal > 0 {
				rates.rate = &tc.personal
				rate = tc.personal
			}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, rates)
			// Pin synthetic catalogue evidence: Priority doubles input/output/read,
			// but NOT writes. The unrelated built-in fallback has different
			// cache ratios and is not an oracle for the screenshot contract.
			svc.billingService = NewBillingService(svc.cfg, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
				"gpt-6.2-astra": {
					InputCostPerToken: 10e-6, InputCostPerTokenPriority: 20e-6,
					OutputCostPerToken: 50e-6, OutputCostPerTokenPriority: 100e-6,
					CacheCreationInputTokenCost: 12.5e-6, CacheCreationInputTokenCostPriority: 12.5e-6,
					CacheReadInputTokenCost: 1e-6, CacheReadInputTokenCostPriority: 2e-6,
					LongContextInputTokenThreshold: 272000,
					LongContextInputCostMultiplier: 2, LongContextOutputCostMultiplier: 1.5,
				},
			}})
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			if tc.model > 0 {
				svc.settingService = modelBillingSettings(t, ModelBillingConfig{Enabled: true, Rules: []ModelBillingRule{{Model: "gpt-6.2-astra", Multiplier: tc.model}}})
				rate *= tc.model
			}
			gid := int64(77)
			tier, effort := "priority", "high"
			in, out, write, write12h, read := 10e-6, 50e-6, 12.5e-6, 25e-6, 1e-6
			hiIn, hiOut, hiWrite, hiWrite12h, hiRead := 20e-6, 75e-6, 25e-6, 50e-6, 2e-6
			maxTokens := 272000
			pricing := ChannelModelPricing{Models: []string{"gpt-6.2-astra"}, BillingMode: BillingModeToken,
				InputPrice: &in, OutputPrice: &out, CacheWritePrice: &write, CacheWrite12hPrice: &write12h, CacheReadPrice: &read,
				Intervals: []PricingInterval{
					{MinTokens: 0, MaxTokens: &maxTokens, InputPrice: &in, OutputPrice: &out, CacheWritePrice: &write, CacheWrite12hPrice: &write12h, CacheReadPrice: &read},
					{MinTokens: 272001, InputPrice: &hiIn, OutputPrice: &hiOut, CacheWritePrice: &hiWrite, CacheWrite12hPrice: &hiWrite12h, CacheReadPrice: &hiRead},
				},
			}
			if tc.reasoning > 0 {
				pricing.ReasoningEffortMultipliers = map[string]float64{"high": tc.reasoning}
			}
			group := &Group{ID: gid, Platform: PlatformOpenAI, Hydrated: true, FreeOpenAIFast: true, RateMultiplier: .5,
				LongContextPricingEnabled: !tc.contextDisabled, ModelPricing: []ChannelModelPricing{pricing}}
			// Group token cards deliberately ignore custom intervals. Exercise
			// the real catalogue whole-request tier and its group gate instead;
			// the disabled case must stay at base rates despite these intervals.
			resolved := svc.resolver.Resolve(context.Background(), PricingInput{Model: "gpt-6.2-astra", GroupID: &gid, Group: group})
			require.Equal(t, PricingSourceGroup, resolved.Source)
			require.Empty(t, resolved.Intervals)
			require.Equal(t, 272000, resolved.BasePricing.LongContextInputThreshold)
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "synthetic-" + tc.name, Model: "gpt-6.2-astra", ServiceTier: &tier, ReasoningEffort: &effort,
					Duration: time.Second, Usage: OpenAIUsage{InputTokens: tc.input + tc.write30m + tc.write12h + tc.read,
						OutputTokens: tc.output, CacheCreationInputTokens: tc.write30m + tc.write12h,
						CacheCreation30mInputTokens: tc.write30m, CacheCreation12hInputTokens: tc.write12h, CacheReadInputTokens: tc.read}},
				APIKey: &APIKey{ID: 1020, GroupID: &gid, Group: group}, User: &User{ID: 2020},
				Account: &Account{ID: 3020, Platform: PlatformOpenAI, Type: kind},
			})
			require.NoError(t, err)
			log := repo.lastLog
			require.NotNil(t, log)
			raw, err := json.Marshal(log)
			require.NoError(t, err)
			var saved map[string]any
			require.NoError(t, json.Unmarshal(raw, &saved))
			d, ok := saved["CustomerCostDetails"].(map[string]any)
			require.True(t, ok, "Free Fast must persist the Standard customer cost snapshot")
			if tc.long {
				in, out, write, write12h, read = hiIn, hiOut, hiWrite, hiWrite12h, hiRead
			}
			reasoning := tc.reasoning
			if reasoning == 0 {
				reasoning = 1
			}
			// Preserve the quotation's operation order: computeTokenBreakdown
			// sums input/output/write/read before reasoning; customer rate is
			// applied before reasoning, then the model surcharge. Random map
			// iteration can straddle the eight-decimal half boundary by 1 ULP.
			expected := []struct {
				field  string
				amount float64
			}{
				{"input_cost", float64(tc.input) * in},
				{"output_cost", float64(tc.output) * out},
				{"cache_creation_cost", float64(tc.write30m)*write + float64(tc.write12h)*write12h},
				{"cache_read_cost", float64(tc.read) * read},
			}
			total := 0.0
			for _, component := range expected {
				require.InDelta(t, component.amount*reasoning, d[component.field], 1e-10, component.field)
				total += component.amount
			}
			customerRate, modelRate := .5, 1.0
			if tc.personal > 0 {
				customerRate = tc.personal
			}
			if tc.model > 0 {
				modelRate = tc.model
			}
			actual := total * customerRate
			actual *= reasoning
			actual *= modelRate
			require.InDelta(t, total*reasoning, d["total_cost"], 1e-10)
			require.InDelta(t, rate, d["multiplier"], 1e-10)
			require.InDelta(t, QuantizeUsageBillingAmount(actual), log.ActualCost, 1e-10)
			if tc.name == "personal model reasoning" {
				// Exact decimal fixture: .0021395 × .3 × 1.7 × 3 =
				// .003273435, rounded half-away-from-zero at eight places.
				require.Equal(t, .00327344, log.ActualCost)
			}
			require.Equal(t, "priority", *log.ServiceTier)
			require.Equal(t, tc.long, log.LongContextBillingApplied)
			require.EqualValues(t, 2, d["version"])
			if tc.write30m+tc.write12h+tc.read > 0 {
				lines, ok := d["cache_cost_details"].([]any)
				require.True(t, ok, "recorded usage must carry the same resolved Standard cache prices")
				expectedPrices := map[string]float64{"write_30m": write * reasoning, "write_12h": write12h * reasoning, "read": read * reasoning}
				expectedTokens := map[string]int{"write_30m": tc.write30m, "write_12h": tc.write12h, "read": tc.read}
				for _, value := range lines {
					line, ok := value.(map[string]any)
					require.True(t, ok, "cache price entry must be an object")
					kind, ok := line["kind"].(string)
					require.True(t, ok, "cache price kind must be a string")
					require.Contains(t, expectedPrices, kind)
					require.InDelta(t, expectedPrices[kind], line["unit_price"], 1e-16)
					require.EqualValues(t, expectedTokens[kind], line["tokens"])
					delete(expectedTokens, kind)
				}
				for _, count := range expectedTokens {
					require.Zero(t, count)
				}
			}
			if tc.name == "exact screenshot" {
				require.InDelta(t, .1669, log.InputCost, 1e-10)
				require.InDelta(t, .0136, log.OutputCost, 1e-10)
				require.InDelta(t, .0149625, log.CacheCreationCost, 1e-10)
				require.InDelta(t, .074938, log.CacheReadCost, 1e-10)
				require.InDelta(t, .2704005, log.TotalCost, 1e-10)
				require.InDelta(t, .07134075, log.ActualCost, 1e-10)
			}
			// The persisted evidence must not change when today's group configuration changes.
			group.FreeOpenAIFast = false
			group.RateMultiplier = 9
			*group.ModelPricing[0].InputPrice = 999
			after, err := json.Marshal(log)
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(after))
		})
	}
}

func TestFreeFastCustomerCostCache1h(t *testing.T) {
	// The shared quote calculation also supports 5m/1h buckets. Snapshot their
	// actual Standard sum; never halve the aggregate Priority cache-write cost.
	billing := &BillingService{}
	price := &ModelPricing{InputPricePerToken: 10e-6, OutputPricePerToken: 50e-6,
		CacheCreationPricePerToken: 12.5e-6, CacheReadPricePerToken: 1e-6,
		SupportsCacheBreakdown: true, CacheCreation5mPrice: 12.5e-6, CacheCreation1hPrice: 20e-6}
	tokens := UsageTokens{InputTokens: 71, OutputTokens: 19, CacheCreationTokens: 24, CacheCreation5mTokens: 11, CacheCreation1hTokens: 13, CacheReadTokens: 17}
	quote := billing.computeTokenBreakdown(price, tokens, .6, "", false)
	detail := newFreeFastCustomerCostDetails(quote, .6)
	require.NotNil(t, detail)
	require.InDelta(t, 11*12.5e-6+13*20e-6, detail.CacheCreationCost, 1e-12)
	require.InDelta(t, 17e-6, detail.CacheReadCost, 1e-12)
	require.InDelta(t, detail.TotalCost*.6, detail.ActualCost, 1e-12)
}

func TestFreeFastCustomerCostControls(t *testing.T) {
	for _, tc := range []struct {
		tier     string
		free     bool
		platform string
	}{
		{"priority", false, PlatformOpenAI}, {"default", true, PlatformOpenAI}, {"flex", true, PlatformOpenAI}, {"priority", true, PlatformAnthropic},
	} {
		t.Run(tc.tier+"/"+tc.platform+"/"+fmt.Sprint(tc.free), func(t *testing.T) {
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			gid := int64(77)
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "synthetic-control", Model: "gpt-5.4", ServiceTier: &tc.tier, Duration: time.Second, Usage: OpenAIUsage{InputTokens: 71, OutputTokens: 19}},
				APIKey: &APIKey{ID: 1020, GroupID: &gid, Group: &Group{ID: gid, Platform: PlatformOpenAI, Hydrated: true, RateMultiplier: .6, FreeOpenAIFast: tc.free}},
				User:   &User{ID: 2020}, Account: &Account{ID: 3020, Platform: tc.platform, Type: AccountTypeOAuth},
			})
			require.NoError(t, err)
			raw, err := json.Marshal(repo.lastLog)
			require.NoError(t, err)
			var saved map[string]any
			require.NoError(t, json.Unmarshal(raw, &saved))
			require.Nil(t, saved["CustomerCostDetails"], "controls must not claim a Free Fast quote")
		})
	}
}

func TestFreeFastCustomerCostCatalogueLongContext(t *testing.T) {
	billing := &BillingService{}
	price := &ModelPricing{InputPricePerToken: 10e-6, OutputPricePerToken: 50e-6,
		CacheCreationPricePerToken: 12.5e-6, CacheReadPricePerToken: 1e-6,
		LongContextInputThreshold: 272000, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 1.5}
	for _, input := range []int{271980, 271981} {
		tokens := UsageTokens{InputTokens: input, OutputTokens: 19, CacheCreationTokens: 11, CacheCreation30mTokens: 11, CacheReadTokens: 9}
		quote := billing.computeTokenBreakdown(price, tokens, .6, "", true)
		detail := newFreeFastCustomerCostDetails(quote, .6)
		require.NotNil(t, detail)
		inputFactor, outputFactor := 1.0, 1.0
		if input > 271980 {
			inputFactor, outputFactor = 2, 1.5
		}
		require.InDelta(t, float64(input)*10e-6*inputFactor, detail.InputCost, 1e-10)
		require.InDelta(t, 19*50e-6*outputFactor, detail.OutputCost, 1e-10)
		require.InDelta(t, 11*12.5e-6*inputFactor, detail.CacheCreationCost, 1e-10)
		require.InDelta(t, 9*1e-6*inputFactor, detail.CacheReadCost, 1e-10)
		require.InDelta(t, detail.TotalCost*.6, detail.ActualCost, 1e-10)
	}
}

func TestFreeFastCustomerCostZeroQuoteKeepsEffectiveRate(t *testing.T) {
	detail := newFreeFastCustomerCostDetails(&CostBreakdown{BillingMode: string(BillingModeToken)}, 1.8)
	require.NotNil(t, detail)
	require.Equal(t, 1.8, detail.Multiplier, "a zero quote must retain the actual resolved rate, not infer 0/0")
	require.Zero(t, detail.TotalCost)
	require.Zero(t, detail.ActualCost)
}

// JSON assertions also compile before cache-price snapshots exist, so remote
// source-overlay RED must fail on missing evidence, never on setup/compilation.
func TestFreeFastCustomerCostCachePrices(t *testing.T) {
	for _, tc := range []struct {
		name             string
		long, zero, hour bool
		reasoning        float64
	}{
		{name: "mixed 30m 12h read"}, {name: "whole context", long: true},
		{name: "reasoning", reasoning: 1.7}, {name: "explicit zero prices", zero: true}, {name: "5m 1h", hour: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read, short, long := 1e-6, 12.5e-6, 25e-6
			if tc.zero {
				read, short, long = 0, 0, 0
			}
			pricing := &ModelPricing{InputPricePerToken: 10e-6, OutputPricePerToken: 50e-6,
				CacheReadPricePerToken: read, CacheCreationPricePerToken: short, CacheCreation12hPricePerToken: &long,
				LongContextInputThreshold: 100, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 1.5}
			tokens := UsageTokens{InputTokens: 71, OutputTokens: 19, CacheReadTokens: 17, CacheCreationTokens: 24, CacheCreation30mTokens: 11, CacheCreation12hTokens: 13}
			kindShort, kindLong := "write_30m", "write_12h"
			if tc.hour {
				pricing.SupportsCacheBreakdown = true
				pricing.CacheCreation5mPrice = short
				pricing.CacheCreation1hPrice = long
				tokens.CacheCreation30mTokens = 0
				tokens.CacheCreation12hTokens = 0
				tokens.CacheCreation5mTokens = 11
				tokens.CacheCreation1hTokens = 13
				kindShort, kindLong = "write_5m", "write_1h"
			}
			quote := (&BillingService{}).computeTokenBreakdown(pricing, tokens, .3, "", tc.long)
			reasoning := tc.reasoning
			if reasoning == 0 {
				reasoning = 1
			}
			applyCostBreakdownMultiplier(quote, reasoning)
			// Customer model surcharge belongs in the effective multiplier, not prices.
			quote.ActualCost *= 3
			snapshot := newFreeFastCustomerCostDetails(quote, .3*3)
			raw, err := json.Marshal(snapshot)
			require.NoError(t, err)
			var detail map[string]any
			require.NoError(t, json.Unmarshal(raw, &detail))
			require.EqualValues(t, 2, detail["version"], "Standard cache prices must be persisted from the resolved quote")
			lines, ok := detail["cache_cost_details"].([]any)
			require.True(t, ok, "Standard cache prices must be persisted from the resolved quote")
			require.Len(t, lines, 3)
			factor := reasoning
			if tc.long {
				factor *= 2
			}
			expected := map[string]struct {
				count int
				price float64
			}{"read": {17, read * factor}, kindShort: {11, short * factor}, kindLong: {13, long * factor}}
			writeSum, readSum := 0.0, 0.0
			for _, value := range lines {
				line, ok := value.(map[string]any)
				require.True(t, ok, "cache price entry must be an object")
				kind, ok := line["kind"].(string)
				require.True(t, ok, "cache price kind must be a string")
				want, exists := expected[kind]
				require.True(t, exists, kind)
				require.EqualValues(t, want.count, line["tokens"])
				require.InDelta(t, want.price, line["unit_price"], 1e-16, kind)
				unitPrice, ok := line["unit_price"].(float64)
				require.True(t, ok, "cache unit price must be numeric")
				count, ok := line["tokens"].(float64)
				require.True(t, ok, "cache token count must be numeric")
				cost := unitPrice * count
				if kind == "read" {
					readSum += cost
				} else {
					writeSum += cost
				}
				delete(expected, kind)
			}
			require.Empty(t, expected)
			require.InDelta(t, quote.CacheCreationCost, writeSum, 1e-12)
			require.InDelta(t, quote.CacheReadCost, readSum, 1e-12)
			// Later policy changes cannot mutate saved price evidence.
			pricing.CacheReadPricePerToken = 999
			long = 999
			after, err := json.Marshal(snapshot)
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(after))
		})
	}
}
