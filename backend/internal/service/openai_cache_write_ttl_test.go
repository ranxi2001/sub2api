//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The mixed case is the sanitized usage and channel price card from #8604.
// Observe the actual response handlers and RecordUsage, including the debit and
// account-statistics cost, rather than reconstructing the billing formula there.
func TestOpenAICacheWriteTTLRecordUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name        string
		short, long int
	}{
		{"30m", 2399, 0},
		{"12h", 0, 2399},
		{"mixed", 9, 2390},
	} {
		for _, route := range []string{"json", "sse", "passthrough_json", "passthrough_sse"} {
			t.Run(tc.name+"/"+route, func(t *testing.T) {
				const groupID int64 = 777
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				userRepo := &openAIRecordUsageUserRepoStub{}
				svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
				var pricing ChannelModelPricing
				require.NoError(t, json.Unmarshal([]byte(`{"platform":"openai","models":["gpt-6-luna"],"billing_mode":"token","input_price":0.00000065,"output_price":0.00000325,"cache_write_price":0.0000008125,"cache_write_12h_price":0.000001625,"cache_read_price":0.000000065}`), &pricing))
				channel := Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{groupID}, ModelPricing: []ChannelModelPricing{pricing}, ApplyPricingToAccountStats: true}
				svc.channelService = &ChannelService{}
				svc.channelService.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{groupID: PlatformOpenAI}))
				svc.resolver = NewModelPricingResolver(svc.channelService, svc.billingService)
				svc.cfg.Gateway.MaxLineSize = defaultMaxLineSize
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				response := fmt.Sprintf(`{"id":"resp_ttl","object":"response","model":"gpt-6-luna","status":"completed","output":[],"usage":{"input_tokens":2402,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":2399,"cache_write_tokens_details":{"ttl_30m_tokens":%d,"ttl_12h_tokens":%d}},"output_tokens":5,"total_tokens":2407}}`, tc.short, tc.long)
				stream := strings.HasSuffix(route, "sse")
				contentType := "application/json"
				if stream {
					response = `data: {"type":"response.completed","response":` + response + "}\n\n"
					contentType = "text/event-stream"
				}
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response))}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				var usage *OpenAIUsage
				switch route {
				case "json":
					result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "gpt-6-luna", "gpt-6-luna")
					require.NoError(t, err)
					usage = result.usage
				case "sse":
					result, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "gpt-6-luna", "gpt-6-luna")
					require.NoError(t, err)
					usage = result.usage
				case "passthrough_json":
					result, err := svc.handleNonStreamingResponsePassthrough(context.Background(), resp, c, account, "gpt-6-luna", "gpt-6-luna")
					require.NoError(t, err)
					usage = result.usage
				case "passthrough_sse":
					result, err := svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "gpt-6-luna", "gpt-6-luna")
					require.NoError(t, err)
					usage = result.usage
				}
				require.NotNil(t, usage)
				err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
					Result: &OpenAIForwardResult{RequestID: "resp_ttl", Model: "gpt-6-luna", Usage: *usage, Stream: stream},
					APIKey: &APIKey{ID: 1, GroupID: i64p(groupID), Group: &Group{ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 0.5}},
					User:   &User{ID: 1}, Account: account,
					ChannelUsageFields: ChannelUsageFields{OriginalModel: "gpt-6-luna", BillingModelSource: BillingModelSourceRequested},
				})
				require.NoError(t, err)
				log := usageRepo.lastLog
				require.NotNil(t, log)
				assert.Equal(t, 3, log.InputTokens)
				assert.Equal(t, 2399, log.CacheCreationTokens)
				assert.Equal(t, tc.short, log.CacheCreation30mTokens)
				assert.Equal(t, tc.long, log.CacheCreation12hTokens)
				assert.Zero(t, log.CacheCreation5mTokens)
				assert.Zero(t, log.CacheCreation1hTokens)
				cacheCost := (float64(tc.short)*0.8125 + float64(tc.long)*1.625) / 1e6
				totalCost := cacheCost + 3*0.65/1e6 + 5*3.25/1e6
				assert.InDelta(t, cacheCost, log.CacheCreationCost, 1e-12)
				assert.InDelta(t, totalCost, log.TotalCost, 1e-12)
				assert.InDelta(t, QuantizeUsageBillingAmount(totalCost*0.5), log.ActualCost, 1e-12)
				assert.InDelta(t, QuantizeUsageBillingAmount(totalCost*0.5), userRepo.lastAmount, 1e-12)
				require.NotNil(t, log.AccountStatsCost)
				assert.InDelta(t, totalCost, *log.AccountStatsCost, 1e-12)
			})
		}
	}
}

func TestOpenAICacheWriteTTLInvalidDetailsKeepAggregate(t *testing.T) {
	for _, details := range []string{
		`null`, `{}`, `{"ttl_12h_tokens":10}`,
		`{"ttl_30m_tokens":0,"ttl_12h_tokens":9}`,
		`{"ttl_30m_tokens":9,"ttl_12h_tokens":9}`,
		`{"ttl_30m_tokens":-1,"ttl_12h_tokens":11}`,
		`{"ttl_30m_tokens":0.5,"ttl_12h_tokens":9.5}`,
		`{"ttl_30m_tokens":"0","ttl_12h_tokens":"10"}`,
	} {
		t.Run(details, func(t *testing.T) {
			usage, ok := openAIUsageFromGJSON(gjson.Parse(`{"input_tokens":10,"input_tokens_details":{"cache_write_tokens":10,"cache_write_tokens_details":` + details + `}}`))
			require.True(t, ok)
			require.Equal(t, 10, usage.CacheCreationInputTokens)
			require.Zero(t, usage.CacheCreation30mInputTokens)
			require.Zero(t, usage.CacheCreation12hInputTokens)
		})
	}
}

func TestOpenAICacheWriteTTLPrices(t *testing.T) {
	service := NewBillingService(nil, nil)
	base, err := service.GetModelPricing("gpt-6-luna")
	require.NoError(t, err)
	tokens := UsageTokens{InputTokens: 3, OutputTokens: 5, CacheCreationTokens: 2399, CacheCreation30mTokens: 9, CacheCreation12hTokens: 2390}

	t.Run("catalog 12h rate", func(t *testing.T) {
		cost := service.computeTokenBreakdown(base, tokens, 1, "", false)
		require.InDelta(t, (9*0.125+2390*0.25)/1e6, cost.CacheCreationCost, 1e-12)
	})
	t.Run("legacy flat channel price", func(t *testing.T) {
		p := *base
		applyChannelTokenPriceOverrides(&p, &ChannelModelPricing{CacheWritePrice: float64Ptr(0.8125e-6)})
		cost := service.computeTokenBreakdown(&p, tokens, 1, "", false)
		require.InDelta(t, 2399*0.8125e-6, cost.CacheCreationCost, 1e-12)
	})
	for _, tier := range []struct {
		name string
		rate float64
	}{{"default", 1}, {"fast", 2}, {"flex", 0.5}} {
		t.Run(tier.name+"/long context and group rate", func(t *testing.T) {
			p := *base
			applyChannelTokenPriceOverrides(&p, &ChannelModelPricing{
				InputPrice: float64Ptr(0.65e-6), OutputPrice: float64Ptr(3.25e-6),
				CacheWritePrice: float64Ptr(0.8125e-6), CacheWrite12hPrice: float64Ptr(1.625e-6),
			})
			p.LongContextInputThreshold = 1
			p.LongContextInputMultiplier = 2
			p.LongContextOutputMultiplier = 1.5
			cost := service.computeTokenBreakdown(&p, tokens, 0.5, tier.name, true)
			cacheCost := (9*0.8125 + 2390*1.625) / 1e6 * 2 * tier.rate
			want := cacheCost + 3*0.65e-6*2*tier.rate + 5*3.25e-6*1.5*tier.rate
			require.InDelta(t, cacheCost, cost.CacheCreationCost, 1e-12)
			require.InDelta(t, want*0.5, cost.ActualCost, 1e-12)
		})
	}
	t.Run("Astra catalog ultrafast", func(t *testing.T) {
		p, err := service.GetModelPricing("gpt-6-astra")
		require.NoError(t, err)
		cost := service.computeTokenBreakdown(p, UsageTokens{CacheCreationTokens: 1_000_000, CacheCreation12hTokens: 1_000_000}, 1, "ultrafast", false)
		require.InDelta(t, 150, cost.CacheCreationCost, 1e-12)
	})
	t.Run("explicit free 12h", func(t *testing.T) {
		p := *base
		applyChannelTokenPriceOverrides(&p, &ChannelModelPricing{CacheWritePrice: float64Ptr(1e-6), CacheWrite12hPrice: float64Ptr(0)})
		cost := service.computeTokenBreakdown(&p, tokens, 1, "", false)
		require.InDelta(t, 9e-6, cost.CacheCreationCost, 1e-12)
	})
	t.Run("explicit free 30m with fast 12h", func(t *testing.T) {
		p := *base
		applyChannelTokenPriceOverrides(&p, &ChannelModelPricing{InputPrice: float64Ptr(0), CacheWritePrice: float64Ptr(0), CacheWrite12hPrice: float64Ptr(2e-6)})
		cost := service.computeTokenBreakdown(&p, tokens, 1, "fast", false)
		require.InDelta(t, 2390*4e-6, cost.CacheCreationCost, 1e-12)
	})
	t.Run("missing TTL does not infer a 12h write", func(t *testing.T) {
		p := *base
		applyChannelTokenPriceOverrides(&p, &ChannelModelPricing{CacheWritePrice: float64Ptr(1e-6), CacheWrite12hPrice: float64Ptr(2e-6)})
		cost := service.computeTokenBreakdown(&p, UsageTokens{CacheCreationTokens: 2399}, 1, "", false)
		require.InDelta(t, 2399e-6, cost.CacheCreationCost, 1e-12)
	})
	t.Run("interval scales both write rates", func(t *testing.T) {
		p := *base
		applyChannelTokenPriceOverrides(&p, &ChannelModelPricing{CacheWritePrice: float64Ptr(1e-6), CacheWrite12hPrice: float64Ptr(2e-6)})
		iv := intervalToModelPricing(&PricingInterval{CacheWriteMultiplier: float64Ptr(3)}, &p, nil)
		cost := service.computeTokenBreakdown(iv, tokens, 1, "fast", false)
		require.InDelta(t, (9*1e-6+2390*2e-6)*3*2, cost.CacheCreationCost, 1e-12)
		require.InDelta(t, 2e-6, *p.CacheCreation12hPricePerToken, 1e-12, "the shared base price is immutable")
	})
	t.Run("account stats custom TTL prices", func(t *testing.T) {
		cost := calculateStatsCost(&ChannelModelPricing{
			BillingMode: BillingModeToken, CacheWritePrice: float64Ptr(1e-6), CacheWrite12hPrice: float64Ptr(2e-6),
		}, tokens, 1)
		require.NotNil(t, cost)
		require.InDelta(t, 9e-6+2390*2e-6, *cost, 1e-12)
	})
}

// Group cards take precedence over channel cards for both the real charge and
// the public schedule. Explicit zero and an absent 12h field stay distinct.
func TestOpenAICacheWriteTTLGroupPricingAndSchedule(t *testing.T) {
	for _, tc := range []struct {
		name      string
		long      *float64
		effective float64
	}{
		{"explicit group price", float64Ptr(3e-6), 3e-6},
		{"explicit free group price", float64Ptr(0), 0},
		{"legacy flat group price", nil, 2e-6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := []ChannelModelPricing{{
				Platform: PlatformOpenAI, Models: []string{"gpt-6-luna"}, BillingMode: BillingModeToken,
				CacheWritePrice: float64Ptr(1e-6), CacheWrite12hPrice: float64Ptr(9e-6),
			}}
			bs, resolver := newTokenCostTestEnv(t, PlatformOpenAI, channel, nil)
			group := &Group{ID: 100, Platform: PlatformOpenAI, ModelPricing: []ChannelModelPricing{{
				Models: []string{"gpt-6-luna"}, BillingMode: BillingModeToken,
				CacheWritePrice: float64Ptr(2e-6), CacheWrite12hPrice: tc.long,
			}}}
			ctx := WithResolvedTargetPlatform(context.Background(), PlatformOpenAI)
			resolved := resolver.Resolve(ctx, PricingInput{Model: "gpt-6-luna", GroupID: &group.ID, Group: group})
			require.Equal(t, PricingSourceGroup, resolved.Source)
			requirePrice(t, tc.long, resolved.BasePricing.CacheCreation12hPricePerToken, "resolved group 12h")
			tokens := UsageTokens{CacheCreationTokens: 2399, CacheCreation30mTokens: 9, CacheCreation12hTokens: 2390}
			cost, err := bs.CalculateTokenCostForRequest(TokenCostRequest{
				Ctx: ctx, Model: "gpt-6-luna", Group: group, Tokens: tokens, RateMultiplier: 0.5,
				Resolver: resolver, Resolved: resolved,
			})
			require.NoError(t, err)
			want := 9*2e-6 + 2390*tc.effective
			require.InDelta(t, want, cost.CacheCreationCost, 1e-12)
			require.InDelta(t, want*0.5, cost.ActualCost, 1e-12)
			schedule, err := bs.ResolveContextPricingSchedule(ctx, resolver, ContextPricingScheduleInput{
				Model: "gpt-6-luna", Group: group, Platform: PlatformOpenAI,
			})
			require.NoError(t, err)
			require.Len(t, schedule.Tiers, 1)
			requirePrice(t, tc.long, schedule.Tiers[0].CacheWrite12h, "schedule group 12h")
			display := plazaPricingFromSchedule(&group.ModelPricing[0], schedule)
			requirePrice(t, tc.long, display.CacheWrite12hPrice, "plaza group 12h")
			requirePrice(t, float64Ptr(9e-6), channel[0].CacheWrite12hPrice, "channel remains unchanged")
		})
	}
}

// A change confined to 12h must remain a separate context tier and reach the
// model-plaza projection; equal generic cache rates do not make tiers equal.
func TestOpenAICacheWriteTTLChannelScheduleKeepsDistinctLongRates(t *testing.T) {
	channel := []ChannelModelPricing{{
		Platform: PlatformOpenAI, Models: []string{"gpt-6-luna"}, BillingMode: BillingModeToken,
		CacheWritePrice: float64Ptr(1e-6), CacheWrite12hPrice: float64Ptr(2e-6),
		Intervals: []PricingInterval{
			{MinTokens: 0, MaxTokens: intPtr(10000), CacheWrite12hPrice: float64Ptr(2e-6)},
			{MinTokens: 10000, CacheWrite12hPrice: float64Ptr(3e-6)},
		},
	}}
	bs, resolver := newTokenCostTestEnv(t, PlatformOpenAI, channel, nil)
	group := enabledGroup(PlatformOpenAI)
	schedule, err := bs.ResolveContextPricingSchedule(context.Background(), resolver, ContextPricingScheduleInput{
		Model: "gpt-6-luna", Group: group, Platform: PlatformOpenAI,
	})
	require.NoError(t, err)
	require.Len(t, schedule.Tiers, 2, "12h-only change must prevent tier merging")
	requirePrice(t, float64Ptr(2e-6), schedule.Tiers[0].CacheWrite12h, "lower 12h")
	requirePrice(t, float64Ptr(3e-6), schedule.Tiers[1].CacheWrite12h, "upper 12h")
	display := plazaPricingFromSchedule(&channel[0], schedule)
	requirePrice(t, float64Ptr(2e-6), display.CacheWrite12hPrice, "plaza base 12h")
	require.Len(t, display.Intervals, 2)
	requirePrice(t, float64Ptr(3e-6), display.Intervals[1].CacheWrite12hPrice, "plaza upper 12h")
}
