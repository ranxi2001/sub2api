//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// newPlazaService 构造 ListGroups 测试用的 ModelPlazaService（不接计费服务：展示定价原样透传）。
func newPlazaService(channels []Channel, groups []Group, pricing *PricingService) *ModelPlazaService {
	repo := &mockChannelRepository{
		listAllFn: func(ctx context.Context) ([]Channel, error) { return channels, nil },
	}
	return NewModelPlazaService(repo, &stubGroupRepoForAvailable{activeGroups: groups}, pricing, nil, nil, nil)
}

func plazaPricedChannel(id int64, name string, groupIDs []int64, platform string, models ...string) Channel {
	return Channel{
		ID:       id,
		Name:     name,
		Status:   StatusActive,
		GroupIDs: groupIDs,
		ModelPricing: []ChannelModelPricing{{
			Platform:    platform,
			Models:      models,
			BillingMode: BillingModeToken,
			InputPrice:  testPtrFloat64(3e-6),
			OutputPrice: testPtrFloat64(1.5e-5),
		}},
	}
}

func TestListPlazaGroups_GroupCentricAggregation(t *testing.T) {
	// 两个渠道挂同一分组:模型并入同一 PlazaGroup;无模型的分组不返回。
	channels := []Channel{
		plazaPricedChannel(1, "chA", []int64{10}, "anthropic", "claude-sonnet"),
		plazaPricedChannel(2, "chB", []int64{10}, "anthropic", "claude-opus"),
	}
	groups := []Group{
		{ID: 10, Name: "g-main", Description: "desc", Platform: "anthropic", RateMultiplier: 1},
		{ID: 20, Name: "g-empty", Platform: "anthropic", RateMultiplier: 0.5},
	}
	svc := newPlazaService(channels, groups, nil)
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1, "无模型的分组不应返回")
	require.Equal(t, int64(10), out[0].ID)
	require.Equal(t, "desc", out[0].Description)
	require.Len(t, out[0].Models, 2)
	// 组内模型按名称排序
	require.Equal(t, "claude-opus", out[0].Models[0].Name)
	require.Equal(t, "claude-sonnet", out[0].Models[1].Name)
}

func TestListPlazaGroups_Fable51HasNoImplicitReasoningMultiplier(t *testing.T) {
	ch := plazaPricedChannel(1, "ch", []int64{10}, "anthropic", "claude-fable-5-1")
	svc := newPlazaService([]Channel{ch}, []Group{{ID: 10, Platform: "anthropic"}}, nil)
	groups, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Len(t, groups[0].Models, 1)
	require.Empty(t, groups[0].Models[0].Pricing.ReasoningEffortMultipliers)
}

func TestListPlazaGroups_DedupFirstWinsWithPricingUpgrade(t *testing.T) {
	// 同名模型:先见者胜;仅当已存条目无定价而新条目有定价时升级替换。
	unpriced := Channel{
		ID: 1, Name: "alpha", Status: StatusActive, GroupIDs: []int64{10},
		// mapping-only → SupportedModels 产出无定价条目
		ModelMapping: map[string]map[string]string{
			"anthropic": {"claude-sonnet": "claude-sonnet"},
		},
	}
	priced := plazaPricedChannel(2, "beta", []int64{10}, "anthropic", "claude-sonnet")
	groups := []Group{{ID: 10, Name: "g", Platform: "anthropic", RateMultiplier: 1}}

	// alpha(无价)按名称序先于 beta(有价):先见者无价,应被有价条目升级。
	svc := newPlazaService([]Channel{priced, unpriced}, groups, nil)
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Models, 1)
	require.NotNil(t, out[0].Models[0].Pricing, "无价条目应被有价条目升级")
	require.NotNil(t, out[0].Models[0].Pricing.InputPrice)
}

func TestListPlazaGroups_PlatformIsolation(t *testing.T) {
	// 渠道同时有 anthropic/openai 定价,anthropic 分组只应看到 anthropic 模型。
	ch := Channel{
		ID: 1, Name: "multi", Status: StatusActive, GroupIDs: []int64{10, 20},
		ModelPricing: []ChannelModelPricing{
			{Platform: "anthropic", Models: []string{"claude-sonnet"}, InputPrice: testPtrFloat64(3e-6)},
			{Platform: "openai", Models: []string{"gpt-5"}, InputPrice: testPtrFloat64(2e-6)},
		},
	}
	groups := []Group{
		{ID: 10, Name: "g-claude", Platform: "anthropic", RateMultiplier: 1},
		{ID: 20, Name: "g-gpt", Platform: "openai", RateMultiplier: 1},
	}
	svc := newPlazaService([]Channel{ch}, groups, nil)
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 2)
	byName := map[string][]PlazaModel{}
	for _, g := range out {
		byName[g.Name] = g.Models
	}
	require.Len(t, byName["g-claude"], 1)
	require.Equal(t, "claude-sonnet", byName["g-claude"][0].Name)
	require.Len(t, byName["g-gpt"], 1)
	require.Equal(t, "gpt-5", byName["g-gpt"][0].Name)
}

func TestListPlazaGroups_CompositeIncludesConfiguredConcretePlatforms(t *testing.T) {
	anthropicPrice := 3e-6
	openAIPrice := 2e-6
	ch := Channel{
		ID: 1, Name: "multi", Status: StatusActive, GroupIDs: []int64{10},
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformAnthropic, Models: []string{"shared-model"}, InputPrice: &anthropicPrice},
			{Platform: PlatformOpenAI, Models: []string{"shared-model"}, InputPrice: &openAIPrice},
			{Platform: "", Models: []string{"empty-platform"}},
			{Platform: PlatformComposite, Models: []string{"nested-composite"}},
			{Platform: "unknown-platform", Models: []string{"unknown-platform"}},
		},
	}
	groups := []Group{{ID: 10, Name: "composite", Platform: PlatformComposite, RateMultiplier: 1}}

	out, err := newPlazaService([]Channel{ch}, groups, nil).ListGroups(context.Background())

	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Models, 2, "only concrete platforms are included and same-named models remain distinct")
	require.Equal(t, PlatformAnthropic, out[0].Models[0].Platform)
	require.Equal(t, PlatformOpenAI, out[0].Models[1].Platform)
	require.InDelta(t, anthropicPrice, *out[0].Models[0].Pricing.InputPrice, 1e-12)
	require.InDelta(t, openAIPrice, *out[0].Models[1].Pricing.InputPrice, 1e-12)
}

func TestListPlazaGroups_CompositeAndOrdinaryGroupsDoNotLeakPlatforms(t *testing.T) {
	ch := Channel{
		ID: 1, Name: "multi", Status: StatusActive, GroupIDs: []int64{10, 20},
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformAnthropic, Models: []string{"claude-sonnet"}, InputPrice: testPtrFloat64(3e-6)},
			{Platform: PlatformOpenAI, Models: []string{"gpt-5"}, InputPrice: testPtrFloat64(2e-6)},
		},
	}
	groups := []Group{
		{ID: 10, Name: "anthropic-only", Platform: PlatformAnthropic, RateMultiplier: 1},
		{ID: 20, Name: "composite", Platform: PlatformComposite, RateMultiplier: 1},
	}

	out, err := newPlazaService([]Channel{ch}, groups, nil).ListGroups(context.Background())

	require.NoError(t, err)
	require.Len(t, out, 2)
	byName := map[string]PlazaGroup{}
	for _, group := range out {
		byName[group.Name] = group
	}
	require.Len(t, byName["anthropic-only"].Models, 1)
	require.Equal(t, []PlazaModel{{
		Name: "claude-sonnet", Platform: PlatformAnthropic, Pricing: byName["anthropic-only"].Models[0].Pricing,
	}}, byName["anthropic-only"].Models)
	require.Len(t, byName["composite"].Models, 2)
	require.Equal(t, []string{"claude-sonnet", "gpt-5"}, []string{
		byName["composite"].Models[0].Name,
		byName["composite"].Models[1].Name,
	})
	require.Equal(t, []string{PlatformAnthropic, PlatformOpenAI}, []string{
		byName["composite"].Models[0].Platform,
		byName["composite"].Models[1].Platform,
	})
}

func TestListPlazaGroups_InactiveChannelSkipped(t *testing.T) {
	inactive := plazaPricedChannel(1, "off", []int64{10}, "anthropic", "claude-sonnet")
	inactive.Status = "inactive"
	groups := []Group{{ID: 10, Name: "g", Platform: "anthropic", RateMultiplier: 1}}
	svc := newPlazaService([]Channel{inactive}, groups, nil)
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestListPlazaGroups_SortedByRateMultiplierAsc(t *testing.T) {
	channels := []Channel{
		plazaPricedChannel(1, "ch", []int64{10, 20, 30}, "anthropic", "claude-sonnet"),
	}
	groups := []Group{
		{ID: 10, Name: "b-standard", Platform: "anthropic", RateMultiplier: 1},
		{ID: 20, Name: "a-standard", Platform: "anthropic", RateMultiplier: 1},
		{ID: 30, Name: "cheap", Platform: "anthropic", RateMultiplier: 0.5},
	}
	svc := newPlazaService(channels, groups, nil)
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 3)
	require.Equal(t, "cheap", out[0].Name, "倍率低者在前")
	require.Equal(t, "a-standard", out[1].Name, "同倍率按名称")
	require.Equal(t, "b-standard", out[2].Name)
}

func TestListPlazaGroups_OfficialPricingFill(t *testing.T) {
	pricingSvc := newStubPricingServiceFromMap(map[string]*LiteLLMModelPricing{
		"claude-sonnet": {
			Mode:                                "chat",
			InputCostPerToken:                   3e-6,
			OutputCostPerToken:                  1.5e-5,
			CacheCreationInputTokenCost:         3.75e-6,
			CacheCreationInputTokenCostAbove1hr: 6e-6,
			CacheReadInputTokenCost:             3e-7,
		},
		"token-absent": {Mode: "image_generation", TokenPricingAbsent: true, OutputCostPerImage: 0.04},
	})
	channels := []Channel{
		plazaPricedChannel(1, "ch", []int64{10}, "anthropic", "claude-sonnet", "unknown-model", "token-absent"),
	}
	groups := []Group{{ID: 10, Name: "g", Platform: "anthropic", RateMultiplier: 1}}
	svc := newPlazaService(channels, groups, pricingSvc)
	// 官方价与计费同源：需要计费服务与解析器（官方参考不查渠道，解析器无需渠道服务）。
	svc.billingService = NewBillingService(&config.Config{}, pricingSvc)
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Models, 3)

	byName := map[string]PlazaModel{}
	for _, m := range out[0].Models {
		byName[m.Name] = m
	}
	// 命中:填充完整官方价(含 1h 缓存写入)
	official := byName["claude-sonnet"].OfficialPricing
	require.NotNil(t, official)
	require.InDelta(t, 3e-6, *official.InputPrice, 1e-12)
	require.InDelta(t, 6e-6, *official.CacheWrite1hPrice, 1e-12)
	require.InDelta(t, 3e-7, *official.CacheReadPrice, 1e-12)
	// 未命中:nil(GetModelPricing 的 claude 系列模糊匹配对非 claude 名不生效)
	require.Nil(t, byName["unknown-model"].OfficialPricing)
	// TokenPricingAbsent 条目不作为官方 token 价展示
	require.Nil(t, byName["token-absent"].OfficialPricing)
}

func TestListPlazaGroups_DeepSeekProAliasOfficialPricing(t *testing.T) {
	pricingSvc := newStubPricingServiceFromMap(map[string]*LiteLLMModelPricing{})
	channels := []Channel{
		plazaPricedChannel(1, "custom", []int64{10}, PlatformDeepseek,
			"deepseek-pro", "deepseek-v4-pro", "deepseek-flash", "deepseek-pro-preview"),
	}
	groups := []Group{{ID: 10, Name: "discount", Platform: PlatformDeepseek, RateMultiplier: 0.5}}
	svc := newPlazaServiceWithBilling(channels, groups, map[int64]string{10: PlatformDeepseek}, pricingSvc)

	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, 0.5, out[0].RateMultiplier)
	require.Len(t, out[0].Models, 4)
	byName := make(map[string]PlazaModel)
	for _, model := range out[0].Models {
		byName[model.Name] = model
		// 官方参考价修正不能替换客户渠道价或预乘分组折扣。
		require.NotNil(t, model.Pricing)
		require.Equal(t, 3e-6, *model.Pricing.InputPrice)
		require.Equal(t, 1.5e-5, *model.Pricing.OutputPrice)
	}
	for _, model := range []string{"deepseek-pro", "deepseek-v4-pro"} {
		official := byName[model].OfficialPricing
		require.NotNil(t, official, model)
		require.InDelta(t, 0.66e-6, *official.InputPrice, 1e-15, model)
		require.InDelta(t, 1.98e-6, *official.OutputPrice, 1e-15, model)
		require.InDelta(t, 0.022e-6, *official.CacheReadPrice, 1e-15, model)
	}
	for _, model := range []string{"deepseek-flash", "deepseek-pro-preview"} {
		official := byName[model].OfficialPricing
		require.NotNil(t, official, model)
		require.InDelta(t, 0.15e-6, *official.InputPrice, 1e-15, model)
		require.InDelta(t, 0.6e-6, *official.OutputPrice, 1e-15, model)
		require.InDelta(t, 0.003e-6, *official.CacheReadPrice, 1e-15, model)
	}
}

func TestListPlazaGroups_GroupImagePriceOverridesChannelPricing(t *testing.T) {
	// 图片计费模型:档位价按实收口径合成(分组图片价 > 渠道档位价 > 渠道默认按次价),
	// 分组独立倍率字段透传;未配图片价的分组保持渠道定价原样。
	perReq := 0.2
	tier4K := 0.3
	imgPrice := 0.02
	channels := []Channel{{
		ID: 1, Name: "img-ch", Status: StatusActive, GroupIDs: []int64{10, 20},
		ModelPricing: []ChannelModelPricing{{
			Platform:        "openai",
			Models:          []string{"gpt-image-2"},
			BillingMode:     BillingModeImage,
			PerRequestPrice: &perReq,
			Intervals:       []PricingInterval{{TierLabel: "4K", PerRequestPrice: &tier4K}},
		}},
	}}
	groups := []Group{
		{ID: 10, Name: "g-media", Platform: "openai", RateMultiplier: 1,
			ImagePrice1K: &imgPrice, ImageRateIndependent: true, ImageRateMultiplier: 1,
			VideoRateIndependent: true, VideoRateMultiplier: 0.7},
		{ID: 20, Name: "g-plain", Platform: "openai", RateMultiplier: 0.1},
	}
	svc := newPlazaService(channels, groups, nil)
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 2)
	byName := map[string]PlazaGroup{}
	for _, g := range out {
		byName[g.Name] = g
	}

	media := byName["g-media"]
	require.True(t, media.ImageRateIndependent)
	require.True(t, media.VideoRateIndependent)
	require.InDelta(t, 0.7, media.VideoRateMultiplier, 1e-9)
	require.InDelta(t, 1.0, media.ImageRateMultiplier, 1e-9)
	require.Len(t, media.Models, 1)
	p := media.Models[0].Pricing
	require.NotNil(t, p)
	require.Len(t, p.Intervals, 3)
	tierPrices := map[string]float64{}
	for _, iv := range p.Intervals {
		require.NotNil(t, iv.PerRequestPrice)
		tierPrices[iv.TierLabel] = *iv.PerRequestPrice
	}
	require.InDelta(t, 0.02, tierPrices["1K"], 1e-9, "1K 用分组图片价")
	require.InDelta(t, 0.2, tierPrices["2K"], 1e-9, "2K 分组未配,回落渠道默认按次价")
	require.InDelta(t, 0.3, tierPrices["4K"], 1e-9, "4K 分组未配,回落渠道档位价")

	plain := byName["g-plain"]
	require.False(t, plain.ImageRateIndependent)
	require.False(t, plain.VideoRateIndependent)
	require.Len(t, plain.Models, 1)
	pp := plain.Models[0].Pricing
	require.NotNil(t, pp)
	require.Len(t, pp.Intervals, 1, "未配分组图片价:渠道定价原样")
	require.InDelta(t, 0.2, *pp.PerRequestPrice, 1e-9)

	// 合成为克隆,渠道原始定价不被修改
	require.Len(t, channels[0].ModelPricing[0].Intervals, 1)
}

func TestListPlazaGroups_GroupModelImagePricingWinsOverGroupImageTiers(t *testing.T) {
	groupImage1K, groupImage2K, groupImage4K := 0.05, 0.055, 0.06
	model1K, model2K, model4K := 0.05, 0.075, 0.1
	channels := []Channel{{
		ID: 1, Name: "img-ch", Status: StatusActive, GroupIDs: []int64{10},
		ModelPricing: []ChannelModelPricing{{
			Platform:        "openai",
			Models:          []string{"yeahzi-image"},
			BillingMode:     BillingModeImage,
			PerRequestPrice: testPtrFloat64(0.2),
		}},
	}}
	groups := []Group{{
		ID: 10, Name: "g", Platform: "openai", RateMultiplier: 1,
		ImagePrice1K: &groupImage1K, ImagePrice2K: &groupImage2K, ImagePrice4K: &groupImage4K,
		ModelPricing: []ChannelModelPricing{{
			Platform:        "openai",
			Models:          []string{"yeahzi-image"},
			BillingMode:     BillingModeImage,
			PerRequestPrice: testPtrFloat64(0.1),
			Intervals: []PricingInterval{
				{TierLabel: "1K", PerRequestPrice: &model1K},
				{TierLabel: "2K", PerRequestPrice: &model2K},
				{TierLabel: "4K", PerRequestPrice: &model4K},
			},
		}},
	}}

	out, err := newPlazaService(channels, groups, nil).ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Models, 1)
	p := out[0].Models[0].Pricing
	require.Equal(t, BillingModeImage, p.BillingMode)
	require.Len(t, p.Intervals, 3)
	tierPrices := map[string]float64{}
	for _, iv := range p.Intervals {
		require.NotNil(t, iv.PerRequestPrice)
		tierPrices[iv.TierLabel] = *iv.PerRequestPrice
	}
	require.InDelta(t, 0.05, tierPrices["1K"], 1e-9)
	require.InDelta(t, 0.075, tierPrices["2K"], 1e-9)
	require.InDelta(t, 0.1, tierPrices["4K"], 1e-9)
}

func TestListPlazaGroups_GroupModelFlatImagePriceWinsOverGroupImageTiers(t *testing.T) {
	groupPrice := 0.2
	channels := []Channel{{
		ID: 1, Name: "img-ch", Status: StatusActive, GroupIDs: []int64{10},
		ModelPricing: []ChannelModelPricing{{
			Platform: "openai", Models: []string{"gemini-3.1-flash-lite-image"},
			BillingMode: BillingModeImage, PerRequestPrice: testPtrFloat64(0.2),
		}},
	}}
	groups := []Group{{
		ID: 10, Name: "g", Platform: "openai", RateMultiplier: 1,
		ImagePrice1K: &groupPrice,
		ModelPricing: []ChannelModelPricing{{
			Platform: "openai", Models: []string{"gemini-3.1-flash-lite-image"},
			BillingMode: BillingModeImage, PerRequestPrice: testPtrFloat64(0.1),
		}},
	}}

	out, err := newPlazaService(channels, groups, nil).ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)
	p := out[0].Models[0].Pricing
	require.Equal(t, BillingModeImage, p.BillingMode)
	require.NotNil(t, p.PerRequestPrice)
	require.InDelta(t, 0.1, *p.PerRequestPrice, 1e-9)
	require.Empty(t, p.Intervals, "模型级固定价不应被分组图片档位覆盖")
}

func TestListPlazaGroups_GroupImagePriceIgnoredForNonImageModes(t *testing.T) {
	// token 模式定价不受分组图片价影响。
	imgPrice := 0.02
	channels := []Channel{plazaPricedChannel(1, "ch", []int64{10}, "openai", "gpt-5")}
	groups := []Group{{ID: 10, Name: "g", Platform: "openai", RateMultiplier: 1, ImagePrice1K: &imgPrice}}
	svc := newPlazaService(channels, groups, nil)
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)
	p := out[0].Models[0].Pricing
	require.NotNil(t, p)
	require.Empty(t, p.Intervals)
	require.NotNil(t, p.InputPrice)
	require.Nil(t, p.PerRequestPrice)
}

func TestListPlazaGroups_RepoErrorsPropagate(t *testing.T) {
	sentinel := errors.New("boom")
	repo := &mockChannelRepository{
		listAllFn: func(ctx context.Context) ([]Channel, error) { return nil, sentinel },
	}
	svc := NewModelPlazaService(repo, &stubGroupRepoForAvailable{}, nil, nil, nil, nil)
	out, err := svc.ListGroups(context.Background())
	require.Nil(t, out)
	require.ErrorIs(t, err, sentinel)

	svc2 := NewModelPlazaService(
		&mockChannelRepository{listAllFn: func(ctx context.Context) ([]Channel, error) { return nil, nil }},
		&stubGroupRepoForAvailable{listActiveErr: sentinel},
		nil, nil, nil, nil,
	)
	out2, err2 := svc2.ListGroups(context.Background())
	require.Nil(t, out2)
	require.ErrorIs(t, err2, sentinel)
}

// newPlazaServiceWithBilling 构造接入计费服务与解析器的广场服务：解析器的渠道服务与广场共用同一份渠道数据。
func newPlazaServiceWithBilling(channels []Channel, groups []Group, groupPlatforms map[int64]string, catalog *PricingService) *ModelPlazaService {
	repo := &mockChannelRepository{
		listAllFn: func(ctx context.Context) ([]Channel, error) { return channels, nil },
		getGroupPlatformsFn: func(ctx context.Context, _ []int64) (map[int64]string, error) {
			return groupPlatforms, nil
		},
	}
	cs := NewChannelService(repo, nil, nil, nil, nil)
	bs := NewBillingService(&config.Config{}, catalog)
	return NewModelPlazaService(repo, &stubGroupRepoForAvailable{activeGroups: groups}, catalog, bs, NewModelPricingResolver(cs, bs), nil)
}

func plazaModelsByName(models []PlazaModel) map[string]PlazaModel {
	out := make(map[string]PlazaModel, len(models))
	for _, m := range models {
		out[m.Name] = m
	}
	return out
}

func TestListGroups_TokenLadderFollowsGroupToggle(t *testing.T) {
	// 同一渠道挂开启/关闭阶梯的两个分组：实付档位随分组开关，官方阶梯不受影响。
	channels := []Channel{{
		ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{10, 20},
		ModelPricing: []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"gpt-5.4"}, BillingMode: BillingModeToken}},
	}}
	groups := []Group{
		{ID: 10, Name: "on", Platform: PlatformOpenAI, RateMultiplier: 1, LongContextPricingEnabled: true},
		{ID: 20, Name: "off", Platform: PlatformOpenAI, RateMultiplier: 2, LongContextPricingEnabled: false},
	}
	svc := newPlazaServiceWithBilling(channels, groups, map[int64]string{10: PlatformOpenAI, 20: PlatformOpenAI},
		newStubPricingServiceFromJSON(t, openAILadderCatalogJSON))
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 2)

	on, off := out[0], out[1]
	require.True(t, on.LongContextPricingEnabled)
	require.False(t, off.LongContextPricingEnabled)

	onModel := on.Models[0]
	require.Equal(t, ContextPricingBasisWholeRequest, onModel.LongContextBasis)
	require.Len(t, onModel.Pricing.Intervals, 2)
	require.Equal(t, "≤272K", onModel.Pricing.Intervals[0].TierLabel)
	require.Equal(t, ">272K", onModel.Pricing.Intervals[1].TierLabel)
	require.InDelta(t, 2.5e-6, *onModel.Pricing.InputPrice, 1e-15)
	require.InDelta(t, 5e-6, *onModel.Pricing.Intervals[1].InputPrice, 1e-15)
	require.InDelta(t, 22.5e-6, *onModel.Pricing.Intervals[1].OutputPrice, 1e-15)
	require.InDelta(t, 5e-6, *onModel.Pricing.Intervals[1].CacheWritePrice, 1e-15)
	require.InDelta(t, 0.5e-6, *onModel.Pricing.Intervals[1].CacheReadPrice, 1e-15)

	offModel := off.Models[0]
	require.Empty(t, offModel.LongContextBasis)
	require.Empty(t, offModel.Pricing.Intervals)
	require.InDelta(t, 2.5e-6, *offModel.Pricing.InputPrice, 1e-15)

	for _, m := range []PlazaModel{onModel, offModel} {
		require.NotNil(t, m.OfficialPricing)
		require.Len(t, m.OfficialPricing.Intervals, 2, "官方阶梯不受分组开关影响")
		require.InDelta(t, 5e-6, *m.OfficialPricing.Intervals[1].InputPrice, 1e-15)
		require.InDelta(t, 2.5e-6, *m.OfficialPricing.InputPrice, 1e-15)
	}
}

func TestListGroups_GeminiCatalogLadderShownWholeRequest(t *testing.T) {
	channels := []Channel{{
		ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{10},
		ModelMapping: map[string]map[string]string{PlatformGemini: {"gemini-2.5-pro": "gemini-2.5-pro"}},
	}}
	groups := []Group{{ID: 10, Name: "g", Platform: PlatformGemini, RateMultiplier: 1, LongContextPricingEnabled: true}}
	svc := newPlazaServiceWithBilling(channels, groups, map[int64]string{10: PlatformGemini},
		newStubPricingServiceFromJSON(t, geminiLadderCatalogJSON))
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)
	m := out[0].Models[0]
	require.Equal(t, ContextPricingBasisWholeRequest, m.LongContextBasis)
	require.Len(t, m.Pricing.Intervals, 2)
	require.Equal(t, "≤200K", m.Pricing.Intervals[0].TierLabel)
	require.Equal(t, ">200K", m.Pricing.Intervals[1].TierLabel)
	require.InDelta(t, 2.5e-6, *m.Pricing.Intervals[1].InputPrice, 1e-15)
	require.InDelta(t, 15e-6, *m.Pricing.Intervals[1].OutputPrice, 1e-15)
	// 官方参考价与实付同源：都来自目录数据的阶梯字段
	require.NotNil(t, m.OfficialPricing)
	require.Len(t, m.OfficialPricing.Intervals, 2)
}

func TestListGroups_GroupTokenCardOverridesChannelPricing(t *testing.T) {
	channels := []Channel{plazaPricedChannel(1, "ch", []int64{10}, PlatformAnthropic, "claude-sonnet-4")}
	groups := []Group{{
		ID: 10, Name: "g", Platform: PlatformAnthropic, RateMultiplier: 1, LongContextPricingEnabled: true,
		ModelPricing: []ChannelModelPricing{{Models: []string{"claude-sonnet-*"}, BillingMode: BillingModeToken, InputPrice: testPtrFloat64(1e-6)}},
	}}
	svc := newPlazaServiceWithBilling(channels, groups, map[int64]string{10: PlatformAnthropic}, nil)
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	m := out[0].Models[0]
	require.InDelta(t, 1e-6, *m.Pricing.InputPrice, 1e-15, "分组价卡优先于渠道平价")
	require.InDelta(t, 15e-6, *m.Pricing.OutputPrice, 1e-15, "卡未配置的项回落目录价")
	require.Empty(t, m.Pricing.Intervals)
}

func TestListGroups_ImageModelKeepsTierSynthesisWithBilling(t *testing.T) {
	channels := []Channel{{
		ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{10},
		ModelPricing: []ChannelModelPricing{{
			Platform: PlatformOpenAI, Models: []string{"gpt-image-2"}, BillingMode: BillingModeImage,
			PerRequestPrice: testPtrFloat64(0.04),
		}},
	}}
	groups := []Group{{
		ID: 10, Name: "g", Platform: PlatformOpenAI, RateMultiplier: 1, LongContextPricingEnabled: true,
		ImagePrice1K: testPtrFloat64(0.02),
	}}
	svc := newPlazaServiceWithBilling(channels, groups, map[int64]string{10: PlatformOpenAI}, nil)
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	m := out[0].Models[0]
	require.Equal(t, BillingModeImage, m.Pricing.BillingMode)
	require.Empty(t, m.LongContextBasis)
	require.Len(t, m.Pricing.Intervals, 3)
	require.InDelta(t, 0.02, *m.Pricing.Intervals[0].PerRequestPrice, 1e-12)
	require.InDelta(t, 0.04, *m.Pricing.Intervals[1].PerRequestPrice, 1e-12)
}

func TestListGroups_CatalogMissingStillShowsChannelFlatPricing(t *testing.T) {
	// 目录查不到的模型：计费按渠道平价（未配置项 $0），广场单档展示渠道平价，官方价为空。
	channels := []Channel{plazaPricedChannel(1, "ch", []int64{10}, PlatformAnthropic, "unknown-model-xyz")}
	groups := []Group{{ID: 10, Name: "g", Platform: PlatformAnthropic, RateMultiplier: 1, LongContextPricingEnabled: true}}
	svc := newPlazaServiceWithBilling(channels, groups, map[int64]string{10: PlatformAnthropic}, nil)
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	m := out[0].Models[0]
	require.NotNil(t, m.Pricing)
	require.InDelta(t, 3e-6, *m.Pricing.InputPrice, 1e-15)
	require.Empty(t, m.Pricing.Intervals)
	require.Nil(t, m.Pricing.CacheWritePrice, "目录无价且渠道未配置 → 无价")
	require.Nil(t, m.OfficialPricing)
}

func TestListGroups_TimePricingPassthrough(t *testing.T) {
	channels := []Channel{{
		ID: 1, Name: "ch", Status: StatusActive, GroupIDs: []int64{10},
		ModelPricing: []ChannelModelPricing{{
			Platform: PlatformDeepseek, Models: []string{"deepseek-chat"}, BillingMode: BillingModeToken,
			InputPrice: testPtrFloat64(0.28e-6), OutputPrice: testPtrFloat64(0.42e-6),
			TimePricing: &ChannelTimePricing{Timezone: "Asia/Shanghai", Periods: []ChannelTimePricingPeriod{
				{StartTime: "00:30", EndTime: "08:30", Multiplier: 0.5},
			}},
		}},
	}}
	groups := []Group{{ID: 10, Name: "cn", Platform: PlatformDeepseek, RateMultiplier: 1, LongContextPricingEnabled: true}}
	svc := newPlazaServiceWithBilling(channels, groups, map[int64]string{10: PlatformDeepseek}, nil)
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	m := out[0].Models[0]
	require.NotNil(t, m.TimePricing)
	require.Equal(t, "Asia/Shanghai", m.TimePricing.Timezone)
	require.Len(t, m.TimePricing.Periods, 1)
	require.InDelta(t, 0.5, m.TimePricing.Periods[0].Multiplier, 1e-12)
	// 展示单价为标准时段价
	require.InDelta(t, 0.28e-6, *m.Pricing.InputPrice, 1e-15)
}

func TestListPlazaGroups_AccountRepoErrorPropagates(t *testing.T) {
	sentinel := errors.New("account repository unavailable")
	svc := newPlazaService(nil, []Group{{ID: 10, Platform: PlatformOpenAI}}, nil)
	svc.gatewayService = &GatewayService{accountRepo: &modelsListAccountRepoStub{err: sentinel}}
	out, err := svc.ListGroups(context.Background())
	require.Nil(t, out)
	require.ErrorIs(t, err, sentinel)
}

func TestListPlazaGroups_AccountModelsWithoutChannels(t *testing.T) {
	groups := []Group{
		{ID: 10, Platform: PlatformOpenAI},
		{ID: 20, Platform: PlatformAnthropic},
		{ID: 30, Platform: PlatformOpenAI}, // no accounts
	}
	svc := newPlazaService(nil, groups, nil)
	svc.gatewayService = &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{
		10: {{ID: 1, Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra"}}}},
		20: {{ID: 2, Platform: PlatformAnthropic, Credentials: map[string]any{"model_mapping": map[string]any{"claude-opus-4-8": "claude-opus-4-8"}}}},
	}}}
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Equal(t, int64(10), out[0].ID)
	require.Equal(t, "gpt-6-astra", out[0].Models[0].Name)
	require.Equal(t, int64(20), out[1].ID)
	require.Equal(t, "claude-opus-4-8", out[1].Models[0].Name)
}

func TestListPlazaGroups_AccountCatalogHonorsGroupAllowlistAndPlatform(t *testing.T) {
	g := Group{ID: 10, Platform: PlatformOpenAI, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"gpt-6-astra"}}}
	ch := plazaPricedChannel(1, "ch", []int64{10}, PlatformOpenAI, "blocked-channel-model")
	svc := newPlazaService([]Channel{ch}, []Group{g}, nil)
	svc.gatewayService = &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{
		10: {
			{ID: 1, Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra", "blocked-account-model": "upstream"}}},
			{ID: 2, Platform: PlatformAnthropic, Credentials: map[string]any{"model_mapping": map[string]any{"claude-opus-4-8": "upstream"}}},
		},
	}}}
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Models, 1)
	require.Equal(t, "gpt-6-astra", out[0].Models[0].Name)
}

func TestListPlazaGroups_AccountCatalogKeepsChannelPricing(t *testing.T) {
	ch := Channel{ID: 1, Name: "video", Status: StatusActive, GroupIDs: []int64{10}, ModelPricing: []ChannelModelPricing{
		{Platform: PlatformGrok, Models: []string{"grok-imagine-video"}, BillingMode: BillingModePerRequest, PerRequestPrice: testPtrFloat64(1.5)},
	}}
	svc := newPlazaService([]Channel{ch}, []Group{{ID: 10, Platform: PlatformGrok}}, nil)
	svc.gatewayService = &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{
		10: {{ID: 1, Platform: PlatformGrok, Credentials: map[string]any{"model_mapping": map[string]any{"grok-imagine-video": "upstream", "grok-4.5": "upstream"}}}},
	}}}
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Models, 2)
	models := plazaModelsByName(out[0].Models)
	require.Equal(t, BillingModePerRequest, models["grok-imagine-video"].Pricing.BillingMode)
	require.Equal(t, 1.5, *models["grok-imagine-video"].Pricing.PerRequestPrice)
}

func TestListPlazaGroups_UnmappedAccountsUseGatewayCatalogs(t *testing.T) {
	svc := newPlazaService(nil, []Group{
		{ID: 10, Platform: PlatformOpenAI},
		{ID: 20, Platform: PlatformAnthropic},
		{ID: 30, Platform: PlatformDeepseek},
	}, nil)
	svc.gatewayService = &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{
		10: {{ID: 1, Platform: PlatformOpenAI}},
		20: {{ID: 2, Platform: PlatformAnthropic}},
		30: {{ID: 3, Platform: PlatformDeepseek}},
	}}}
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 2, "CN provider without mapped models must not advertise Claude defaults")
	require.Contains(t, plazaModelsByName(out[0].Models), "gpt-6-astra")
	require.Contains(t, plazaModelsByName(out[1].Models), "claude-opus-4-8")
}

func TestListPlazaGroups_WildcardsAreNotModelCards(t *testing.T) {
	svc := newPlazaService(nil, []Group{{ID: 10, Platform: PlatformAnthropic}}, nil)
	svc.gatewayService = &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{
		10: {{ID: 1, Platform: PlatformAnthropic, Credentials: map[string]any{"model_mapping": map[string]any{"claude-*": "*", "claude-opus-4-8": "upstream"}}}},
	}}}
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Models, 1)
	require.Equal(t, "claude-opus-4-8", out[0].Models[0].Name)
}

func TestListPlazaGroups_MixedGeminiAccounts(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint(mixed), func(t *testing.T) {
			svc := newPlazaService(nil, []Group{{ID: 10, Platform: PlatformGemini}}, nil)
			svc.gatewayService = &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{
				10: {{ID: 1, Platform: PlatformAntigravity,
					Extra: map[string]any{"mixed_scheduling": mixed},
					Credentials: map[string]any{"model_mapping": map[string]any{
						"gemini-3-pro-preview": "upstream", "claude-opus-4-8": "upstream",
					}},
				}},
			}}}
			out, err := svc.ListGroups(context.Background())
			require.NoError(t, err)
			if !mixed {
				require.Empty(t, out)
				return
			}
			require.Len(t, out, 1)
			require.Contains(t, plazaModelsByName(out[0].Models), "gemini-3-pro-preview")
			require.NotContains(t, plazaModelsByName(out[0].Models), "claude-opus-4-8")
			for _, model := range out[0].Models {
				require.Equal(t, PlatformGemini, model.Platform)
			}
		})
	}
}

func TestListPlazaGroups_DefaultCatalogHonorsAccountGroupLimits(t *testing.T) {
	for _, tc := range []struct {
		name           string
		platform       string
		allowed        []string
		groupAllowlist GroupModelAllowlist
		wantEmpty      bool
	}{
		{name: "wildcard restriction", platform: PlatformAnthropic, allowed: []string{"claude-haiku-*"}},
		{name: "composite restriction", platform: PlatformComposite, allowed: []string{"claude-haiku-*"}},
		{
			name: "group allowlist cannot restore blocked models", platform: PlatformAnthropic,
			allowed:        []string{"claude-haiku-*"},
			groupAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"claude-haiku-4-5-20251001", "claude-opus-4-8"}},
		},
		{name: "no matching defaults", platform: PlatformAnthropic, allowed: []string{"unknown-*"}, wantEmpty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := Group{ID: 10, Platform: tc.platform, ModelAllowlist: tc.groupAllowlist}
			svc := newPlazaService(nil, []Group{g}, nil)
			svc.gatewayService = &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{
				10: {{ID: 1, Platform: PlatformAnthropic, AccountGroups: []AccountGroup{{GroupID: 10, AllowedModels: tc.allowed}}}},
			}}}
			out, err := svc.ListGroups(context.Background())
			require.NoError(t, err)
			if tc.wantEmpty {
				require.Empty(t, out)
				return
			}
			require.Len(t, out, 1)
			models := plazaModelsByName(out[0].Models)
			require.Contains(t, models, "claude-haiku-4-5-20251001")
			require.NotContains(t, models, "claude-opus-4-8")
			require.NotContains(t, models, "claude-sonnet-4-6")
			for _, model := range out[0].Models {
				require.Contains(t, model.Name, "claude-haiku-")
				require.Equal(t, PlatformAnthropic, model.Platform)
			}
		})
	}
}

func TestListPlazaGroups_AccountGroupLimitsPreserveOtherAccountsAndChannels(t *testing.T) {
	g := Group{ID: 10, Platform: PlatformAnthropic, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"claude-*", "channel-only"}}}
	ch := plazaPricedChannel(1, "channel", []int64{10}, PlatformAnthropic, "channel-only")
	svc := newPlazaService([]Channel{ch}, []Group{g}, nil)
	svc.gatewayService = &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{
		10: {
			{
				ID: 1, Platform: PlatformAnthropic,
				Credentials:   map[string]any{"model_mapping": map[string]any{"claude-haiku-4-5-20251001": "upstream"}},
				AccountGroups: []AccountGroup{{GroupID: 10, AllowedModels: []string{"claude-haiku-*"}}},
			},
			{
				ID: 2, Platform: PlatformAnthropic,
				Credentials:   map[string]any{"model_mapping": map[string]any{"claude-sonnet-4-6": "upstream"}},
				AccountGroups: []AccountGroup{{GroupID: 10, AllowedModels: []string{"claude-sonnet-*"}}},
			},
			{ID: 3, Platform: PlatformOpenAI}, // another platform cannot authorize Claude defaults
		},
	}}}
	out, err := svc.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, out, 1)
	models := plazaModelsByName(out[0].Models)
	require.Len(t, models, 3)
	require.Contains(t, models, "claude-haiku-4-5-20251001")
	require.Contains(t, models, "claude-sonnet-4-6")
	require.Contains(t, models, "channel-only")
	require.NotContains(t, models, "claude-opus-4-8")
}
