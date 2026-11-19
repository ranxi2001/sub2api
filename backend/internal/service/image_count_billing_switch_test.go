//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func boolPtrForImageBillingTest(v bool) *bool { return &v }

func TestImageCountBillingApplies(t *testing.T) {
	enabled := &APIKey{Group: &Group{ImageCountBilling: true}}
	disabled := &APIKey{Group: &Group{ImageCountBilling: false}}

	require.True(t, imageCountBillingApplies(enabled, true))
	require.False(t, imageCountBillingApplies(enabled, false), "混合请求不能受开关影响")
	require.False(t, imageCountBillingApplies(disabled, true))
	require.False(t, imageCountBillingApplies(nil, true))
}

// Fork adaptation: explicit group token pricing keeps precedence over count fallback.
func TestCalculateRecordUsageCost_ImageCountBillingGroupTokenPricing(t *testing.T) {
	groupID := int64(901)
	model := "gpt-image-2"
	resolver := newOpenAITokenImageChannelPricingResolverForTest(t, groupID, model)
	group := &Group{
		ID:                   groupID,
		Platform:             PlatformComposite,
		Status:               StatusActive,
		Hydrated:             true,
		AllowImageGeneration: true,
		ImageCountBilling:    true,
		ImagePrice1K:         f64p(0.5),
		ModelPricing: []ChannelModelPricing{{
			Models:      []string{model},
			BillingMode: BillingModeToken,
			InputPrice:  f64p(3e-6),
			OutputPrice: f64p(15e-6),
		}},
	}
	apiKey := &APIKey{ID: 1, GroupID: &groupID, Group: group}
	svc := &GatewayService{billingService: resolver.billingService, resolver: resolver}

	cost := svc.calculateRecordUsageCost(context.Background(), &ForwardResult{
		Model: model, ImageCount: 2, ImageSize: "1K", ImageEndpoint: true,
		Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 1000},
	}, apiKey, model, 1, 1, time.Time{})

	require.NotNil(t, cost)
	require.InDelta(t, 0.018, cost.TotalCost, 1e-12, "显式分组 token 价不得被按张开关覆盖")
	require.Equal(t, string(BillingModeToken), cost.BillingMode)
}

// Fork adaptation: channel token pricing remains authoritative with the switch enabled.
func TestCalculateRecordUsageCost_ImageCountBillingChannelTokenPricing(t *testing.T) {
	groupID := int64(902)
	model := "gpt-image-2"
	resolver := newOpenAITokenImageChannelPricingResolverForTest(t, groupID, model)
	group := &Group{
		ID:                   groupID,
		Platform:             PlatformOpenAI,
		Status:               StatusActive,
		Hydrated:             true,
		AllowImageGeneration: true,
		ImageCountBilling:    true,
		ImagePrice1K:         f64p(0.4),
	}
	apiKey := &APIKey{ID: 2, GroupID: &groupID, Group: group}
	svc := &GatewayService{billingService: resolver.billingService, resolver: resolver}

	cost := svc.calculateRecordUsageCost(context.Background(), &ForwardResult{
		Model: model, ImageCount: 2, ImageSize: "1K", ImageEndpoint: true,
		Usage: ClaudeUsage{InputTokens: 1_000_000, OutputTokens: 10_000},
	}, apiKey, model, 1, 1, time.Time{})

	require.NotNil(t, cost)
	expected, err := resolver.billingService.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: model, GroupID: &groupID, Group: group,
		Tokens:       UsageTokens{InputTokens: 1_000_000, OutputTokens: 10_000},
		RequestCount: 1, RateMultiplier: 1, Resolver: resolver,
	})
	require.NoError(t, err)
	require.InDelta(t, expected.TotalCost, cost.TotalCost, 1e-12)
	require.NotEqual(t, 0.8, cost.TotalCost, "不能变成按图片价结算")
	require.Equal(t, string(BillingModeToken), cost.BillingMode)
}

// F4：开关只作用于图片端点，混合请求仍按 token 计费。
func TestCalculateRecordUsageCost_ImageCountBillingIgnoresMixedRequests(t *testing.T) {
	groupID := int64(903)
	model := "gpt-image-2"
	resolver := newOpenAITokenImageChannelPricingResolverForTest(t, groupID, model)
	group := &Group{
		ID:                   groupID,
		Platform:             PlatformComposite,
		Status:               StatusActive,
		Hydrated:             true,
		AllowImageGeneration: true,
		ImageCountBilling:    true,
		ImagePrice1K:         f64p(0.4),
	}
	apiKey := &APIKey{ID: 3, GroupID: &groupID, Group: group}
	svc := &GatewayService{billingService: resolver.billingService, resolver: resolver}

	cost := svc.calculateRecordUsageCost(context.Background(), &ForwardResult{
		Model: model, ImageCount: 1, ImageSize: "1K", ImageEndpoint: false,
		Usage: ClaudeUsage{InputTokens: 1_000_000, OutputTokens: 10_000},
	}, apiKey, model, 1, 1, time.Time{})

	require.NotNil(t, cost)
	require.Equal(t, string(BillingModeToken), cost.BillingMode)
	require.Greater(t, cost.TotalCost, 0.0)
}

func TestCalculateOpenAIRecordUsageCost_ImageCountBillingIgnoresMixedRequests(t *testing.T) {
	groupID := int64(904)
	model := "gpt-image-2"
	resolver := newOpenAITokenImageChannelPricingResolverForTest(t, groupID, model)
	group := &Group{
		ID:                   groupID,
		Platform:             PlatformOpenAI,
		Status:               StatusActive,
		Hydrated:             true,
		AllowImageGeneration: true,
		ImageCountBilling:    true,
		ImagePrice1K:         f64p(0.4),
	}
	apiKey := &APIKey{ID: 4, GroupID: &groupID, Group: group}
	svc := &OpenAIGatewayService{billingService: resolver.billingService, resolver: resolver}

	cost, err := svc.calculateOpenAIRecordUsageCost(context.Background(), &OpenAIForwardResult{
		Model: model, ImageCount: 1, ImageSize: "1K", ImageEndpoint: false,
	}, apiKey, []string{model}, 1, 1, 1, 1,
		UsageTokens{InputTokens: 1_000_000, OutputTokens: 10_000},
		"", nil, false, time.Time{})

	require.NoError(t, err)
	require.NotNil(t, cost)
	require.Equal(t, string(BillingModeToken), cost.BillingMode)
	require.Greater(t, cost.TotalCost, 0.0)
}

func TestGroupService_ImageCountBillingValidation(t *testing.T) {
	ctx := context.Background()

	t.Run("创建入口固定 anthropic 平台，拒绝开关", func(t *testing.T) {
		svc := NewGroupService(nil, nil)
		_, err := svc.Create(ctx, CreateGroupRequest{Name: "g", ImageCountBilling: true})
		require.ErrorContains(t, err, "image_count_billing")
	})

	t.Run("更新时未开启图片生成则拒绝", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{getByID: &Group{
			ID: 1, Platform: PlatformComposite, AllowImageGeneration: false,
		}}
		svc := NewGroupService(repo, nil)
		_, err := svc.Update(ctx, 1, UpdateGroupRequest{ImageCountBilling: boolPtrForImageBillingTest(true)})
		require.ErrorContains(t, err, "image_count_billing")
	})

	t.Run("更新时不支持的平台拒绝", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{getByID: &Group{
			ID: 2, Platform: PlatformAnthropic, AllowImageGeneration: true,
		}}
		svc := NewGroupService(repo, nil)
		_, err := svc.Update(ctx, 2, UpdateGroupRequest{ImageCountBilling: boolPtrForImageBillingTest(true)})
		require.ErrorContains(t, err, "image_count_billing")
	})

	t.Run("合法开启", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{getByID: &Group{
			ID: 3, Platform: PlatformComposite, AllowImageGeneration: true,
		}}
		svc := NewGroupService(repo, nil)
		updated, err := svc.Update(ctx, 3, UpdateGroupRequest{ImageCountBilling: boolPtrForImageBillingTest(true)})
		require.NoError(t, err)
		require.True(t, updated.ImageCountBilling)
	})

	t.Run("关闭图片生成时自动清理开关", func(t *testing.T) {
		repo := &groupRepoStubForAdmin{getByID: &Group{
			ID: 4, Platform: PlatformComposite, AllowImageGeneration: false, ImageCountBilling: true,
		}}
		svc := NewGroupService(repo, nil)
		updated, err := svc.Update(ctx, 4, UpdateGroupRequest{})
		require.NoError(t, err)
		require.False(t, updated.ImageCountBilling)
	})
}

// 渠道按次定价只配置按上下文分档（无尺寸档）时，尺寸档未命中，必须用 token
// 上下文匹配档位；漏传 Tokens 会让档位永不命中而按 $0 结算。与开关无关。
func TestCalculateRecordUsageCost_ChannelPerRequestContextTierUsesTokens(t *testing.T) {
	for _, switchOn := range []bool{false, true} {
		groupID := int64(905)
		model := "gemini-3-pro-image"
		resolver := newOpenAITokenImageChannelPricingResolverForTest(t, groupID, model)
		cached, ok := resolver.channelService.cache.Load().(*channelCache)
		require.True(t, ok)
		maxTokens := 1000
		cached.pricingByGroupModel[channelModelKey{groupID: groupID, model: model}] = &ChannelModelPricing{
			BillingMode: BillingModePerRequest,
			Intervals: []PricingInterval{
				{MinTokens: 0, MaxTokens: &maxTokens, PerRequestPrice: f64p(0.1)},
				{MinTokens: 1000, PerRequestPrice: f64p(0.3)},
			},
		}
		group := &Group{
			ID:                   groupID,
			Platform:             PlatformComposite,
			Status:               StatusActive,
			Hydrated:             true,
			AllowImageGeneration: true,
			ImageCountBilling:    switchOn,
		}
		apiKey := &APIKey{ID: 5, GroupID: &groupID, Group: group}
		svc := &GatewayService{billingService: resolver.billingService, resolver: resolver}

		cost := svc.calculateRecordUsageCost(context.Background(), &ForwardResult{
			Model: model, ImageCount: 2, ImageSize: "1K", ImageEndpoint: true,
			Usage: ClaudeUsage{InputTokens: 5000, OutputTokens: 100},
		}, apiKey, model, 1, 1, time.Time{})

		require.NotNil(t, cost)
		require.InDelta(t, 0.6, cost.TotalCost, 1e-12, "switch=%v: 应按上下文档 0.3 × 2 张结算", switchOn)
	}
}

func TestImageCountBillingUsesFallbackWithoutOperatorTokenCard(t *testing.T) {
	groupID := int64(906)
	group := &Group{ID: groupID, Platform: PlatformOpenAI, AllowImageGeneration: true, ImageCountBilling: true, ImagePrice1K: f64p(0.4)}
	key := &APIKey{GroupID: &groupID, Group: group}
	svc := &GatewayService{billingService: NewBillingService(nil, nil)}
	cost := svc.calculateRecordUsageCost(context.Background(), &ForwardResult{Model: "gpt-image-2", ImageCount: 3, ImageSize: "1K", ImageEndpoint: true}, key, "gpt-image-2", 1, 1, time.Time{})
	require.InDelta(t, 1.2, cost.TotalCost, 1e-12)
	require.Equal(t, string(BillingModeImage), cost.BillingMode)
}

// Both APIs must retain operator token-card priority for dedicated and mixed
// requests. Fixed expected costs make changing only the routing branch observable.
func TestImageCountBillingPreservesExplicitTokenCardsAcrossGateways(t *testing.T) {
	for _, source := range []string{PricingSourceGroup, PricingSourceChannel} {
		for _, imageEndpoint := range []bool{false, true} {
			groupID := int64(907)
			model := "gpt-image-2"
			resolver := newOpenAITokenImageChannelPricingResolverForTest(t, groupID, model)
			group := &Group{ID: groupID, Platform: PlatformOpenAI, Hydrated: true, Status: StatusActive,
				AllowImageGeneration: true, ImageCountBilling: true, ImagePrice1K: f64p(0.4)}
			want := 0.00045 // 100 * $3/MTok + 10 * $15/MTok
			if source == PricingSourceGroup {
				group.ModelPricing = []ChannelModelPricing{{Models: []string{model}, BillingMode: BillingModeToken,
					InputPrice: f64p(7e-6), OutputPrice: f64p(11e-6)}}
				want = 0.00081 // group card beats both channel card and count fallback
			}
			key := &APIKey{GroupID: &groupID, Group: group}
			claude := &GatewayService{billingService: resolver.billingService, resolver: resolver}
			cost := claude.calculateRecordUsageCost(context.Background(), &ForwardResult{
				Model: model, ImageCount: 3, ImageSize: "1K", ImageEndpoint: imageEndpoint,
				Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 10},
			}, key, model, 1, 1, time.Time{})
			require.Equal(t, string(BillingModeToken), cost.BillingMode)
			require.InDelta(t, want, cost.TotalCost, 1e-12)
			openai := &OpenAIGatewayService{billingService: resolver.billingService, resolver: resolver}
			cost, err := openai.calculateOpenAIRecordUsageCost(context.Background(), &OpenAIForwardResult{
				Model: model, ImageCount: 3, ImageSize: "1K", ImageEndpoint: imageEndpoint,
			}, key, []string{model}, 1, 1, 1, 1,
				UsageTokens{InputTokens: 100, OutputTokens: 10}, "", nil, false, time.Time{})
			require.NoError(t, err)
			require.Equal(t, string(BillingModeToken), cost.BillingMode)
			require.InDelta(t, want, cost.TotalCost, 1e-12)
		}
	}
}

func TestExplicitImageCardDoesNotChargeEmptyResponses(t *testing.T) {
	groupID := int64(990)
	model := "gpt-image-2"
	resolver := newOpenAITokenImageChannelPricingResolverForTest(t, groupID, model)
	group := &Group{ID: groupID, Platform: PlatformOpenAI, ModelPricing: []ChannelModelPricing{{Models: []string{model}, Platform: PlatformOpenAI, BillingMode: BillingModeImage, PerRequestPrice: f64p(.134)}}}
	key := &APIKey{GroupID: &groupID, Group: group}
	svc := &OpenAIGatewayService{billingService: resolver.billingService, resolver: resolver}
	for _, count := range []int{0, 2} {
		cost, err := svc.calculateOpenAIRecordUsageCost(context.Background(), &OpenAIForwardResult{Model: model, ImageCount: count, ImageSize: "1K"}, key, []string{model}, 1, 1, 1, 1, UsageTokens{InputTokens: 200, OutputTokens: 300}, "", nil, false, time.Time{})
		require.NoError(t, err)
		require.InDelta(t, float64(count)*.134, cost.ActualCost, 1e-12)
		require.Equal(t, "image", cost.BillingMode, "observed token usage must not charge images again")
	}
}
