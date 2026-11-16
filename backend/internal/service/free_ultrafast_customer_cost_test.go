package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFreeUltrafastCustomerCost(t *testing.T) {
	for _, tc := range []struct {
		name, requested, observed, groupPlatform string
		freeUltra, freeFast, responseBilling     bool
		billingTier                              string
		customerFactor, upstreamFactor           float64
		snapshot                                 bool
	}{
		{name: "default off retains sixfold", requested: "ultrafast", billingTier: "ultrafast", customerFactor: 6, upstreamFactor: 6},
		{name: "free Fast does not cover UltraFast", requested: "ultrafast", freeFast: true, billingTier: "ultrafast", customerFactor: 6, upstreamFactor: 6},
		{name: "explicit UltraFast", requested: "ultrafast", freeUltra: true, billingTier: "ultrafast", customerFactor: 1, upstreamFactor: 6, snapshot: true},
		{name: "observed UltraFast opt in", requested: "priority", observed: "ultrafast", responseBilling: true, freeUltra: true, billingTier: "ultrafast", customerFactor: 1, upstreamFactor: 6, snapshot: true},
		{name: "untrusted response cannot upgrade", requested: "priority", observed: "ultrafast", freeUltra: true, billingTier: "priority", customerFactor: 2, upstreamFactor: 2},
		{name: "free UltraFast does not cover priority", requested: "priority", freeUltra: true, billingTier: "priority", customerFactor: 2, upstreamFactor: 2},
		{name: "both switches retain Free Fast", requested: "priority", freeUltra: true, freeFast: true, billingTier: "priority", customerFactor: 1, upstreamFactor: 2, snapshot: true},
		{name: "downgraded response uses Standard", requested: "ultrafast", observed: "default", freeUltra: true, billingTier: "default", customerFactor: 1, upstreamFactor: 1},
		{name: "composite OpenAI route", requested: "ultrafast", groupPlatform: PlatformComposite, freeUltra: true, billingTier: "ultrafast", customerFactor: 1, upstreamFactor: 6, snapshot: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			personal := 0.55
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, &openAIUserGroupRateRepoStub{rate: &personal})
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			platform := tc.groupPlatform
			if platform == "" {
				platform = PlatformOpenAI
			}
			in, out, write, read := 10e-6, 50e-6, 12.5e-6, 1e-6
			group := &Group{ID: 77, Platform: platform, Hydrated: true, RateMultiplier: .6, FreeOpenAIFast: tc.freeFast,
				ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-5.4"}, BillingMode: BillingModeToken, InputPrice: &in, OutputPrice: &out, CacheWritePrice: &write, CacheReadPrice: &read}}}
			require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{"FreeOpenAIUltrafast":%t}`, tc.freeUltra)), group))
			account := &Account{ID: 3020, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIResponseTierBillingEnabledKey: tc.responseBilling}}
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "synthetic-ultra-" + tc.name, Model: "gpt-5.4", ServiceTier: &tc.requested, UpstreamResponseServiceTier: tc.observed, Usage: OpenAIUsage{InputTokens: 71, OutputTokens: 19}, Duration: time.Second},
				APIKey: &APIKey{ID: 1020, GroupID: &group.ID, Group: group}, User: &User{ID: 2020}, Account: account})
			require.NoError(t, err)
			log := repo.lastLog
			require.NotNil(t, log)
			standard := 71*in + 19*out
			require.InDelta(t, standard*tc.upstreamFactor, log.TotalCost, 1e-12)
			require.Equal(t, QuantizeUsageBillingAmount(standard*tc.customerFactor*personal), log.ActualCost)
			require.Equal(t, tc.billingTier, *log.ServiceTier)
			require.False(t, group.ForceOpenAIFast, "customer discount must not force an upstream tier")
			var saved struct{ CustomerCostDetails json.RawMessage }
			encoded, err := json.Marshal(log)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(encoded, &saved))
			if !tc.snapshot {
				require.True(t, len(saved.CustomerCostDetails) == 0 || string(saved.CustomerCostDetails) == "null")
				return
			}
			var detail struct {
				FreeUltrafast bool    `json:"free_ultrafast"`
				FreeFast      bool    `json:"free_fast"`
				TotalCost     float64 `json:"total_cost"`
				Multiplier    float64 `json:"multiplier"`
			}
			require.NotEmpty(t, saved.CustomerCostDetails)
			require.NoError(t, json.Unmarshal(saved.CustomerCostDetails, &detail))
			require.Equal(t, tc.billingTier == "ultrafast", detail.FreeUltrafast)
			require.Equal(t, tc.billingTier == "priority", detail.FreeFast)
			require.InDelta(t, standard, detail.TotalCost, 1e-12)
			require.InDelta(t, personal, detail.Multiplier, 1e-12)
			before, err := json.Marshal(detail)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal([]byte(`{"FreeOpenAIUltrafast":false}`), group))
			group.RateMultiplier = 9
			after, err := json.Marshal(detail)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after), "historical costs must not depend on current group switches")
		})
	}
}

func TestFreeUltrafastMixedCacheAndLongContext(t *testing.T) {
	for _, long := range []bool{false, true} {
		name := "base"
		ordinary := 71
		if long {
			name = "long"
			ordinary = 272001
		}
		t.Run(name, func(t *testing.T) {
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.billingService = NewBillingService(svc.cfg, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
				"gpt-6.2-astra": {InputCostPerToken: 10e-6, OutputCostPerToken: 50e-6, CacheCreationInputTokenCost: 12.5e-6, CacheReadInputTokenCost: 1e-6, LongContextInputTokenThreshold: 272000, LongContextInputCostMultiplier: 2, LongContextOutputCostMultiplier: 1.5},
			}})
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			in, out, write, write12h, read := 10e-6, 50e-6, 12.5e-6, 25e-6, 1e-6
			group := &Group{ID: 77, Platform: PlatformOpenAI, Hydrated: true, RateMultiplier: .6, LongContextPricingEnabled: true,
				ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-6.2-astra"}, BillingMode: BillingModeToken, InputPrice: &in, OutputPrice: &out, CacheWritePrice: &write, CacheWrite12hPrice: &write12h, CacheReadPrice: &read}}}
			require.NoError(t, json.Unmarshal([]byte(`{"FreeOpenAIUltrafast":true}`), group))
			tier := "ultrafast"
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "synthetic-ultra-cache-" + name, Model: "gpt-6.2-astra", ServiceTier: &tier, Duration: time.Second,
					Usage: OpenAIUsage{InputTokens: ordinary + 11 + 13 + 17, OutputTokens: 19, CacheCreationInputTokens: 24, CacheCreation30mInputTokens: 11, CacheCreation12hInputTokens: 13, CacheReadInputTokens: 17}},
				APIKey: &APIKey{ID: 1020, GroupID: &group.ID, Group: group}, User: &User{ID: 2020}, Account: &Account{ID: 3020, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}})
			require.NoError(t, err)
			log := repo.lastLog
			require.NotNil(t, log)
			inputFactor, outputFactor := 1.0, 1.0
			if long {
				inputFactor = 2
				outputFactor = 1.5
			}
			standard := (float64(ordinary)*in+11*write+13*write12h+17*read)*inputFactor + 19*out*outputFactor
			require.InDelta(t, standard*6, log.TotalCost, 1e-9)
			require.Equal(t, QuantizeUsageBillingAmount(standard*.6), log.ActualCost)
			var saved struct{ CustomerCostDetails map[string]any }
			encoded, err := json.Marshal(log)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(encoded, &saved))
			detail := saved.CustomerCostDetails
			require.NotNil(t, detail)
			require.Equal(t, true, detail["free_ultrafast"])
			require.EqualValues(t, 2, detail["version"])
			require.Len(t, detail["cache_cost_details"], 3)
			require.InDelta(t, (11*write+13*write12h)*inputFactor, detail["cache_creation_cost"], 1e-12)
			require.InDelta(t, 17*read*inputFactor, detail["cache_read_cost"], 1e-12)
			require.Equal(t, long, log.LongContextBillingApplied)
		})
	}
}

func TestFreeUltrafastDoesNotForceOutboundTier(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	group := &Group{ID: 77, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true}
	require.NoError(t, json.Unmarshal([]byte(`{"FreeOpenAIUltrafast":true}`), group))
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	for _, tier := range []string{"", "priority", "ultrafast"} {
		body := []byte(`{"model":"gpt-5.6-sol","input":"synthetic"}`)
		if tier != "" {
			body = []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","service_tier":%q}`, tier))
		}
		after, err := svc.applyOpenAIFastPolicyToBody(ctx, account, "gpt-5.6-sol", body)
		require.NoError(t, err)
		require.Equal(t, tier, gjson.GetBytes(after, "service_tier").String())
	}
}
