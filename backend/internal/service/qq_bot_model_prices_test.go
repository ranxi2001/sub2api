package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type qqPriceGroupRepo struct {
	GroupRepository
	groups []Group
}

func (p qqPriceGroupRepo) ListActive(context.Context) ([]Group, error) { return p.groups, nil }

type qqPriceUserRepo struct {
	UserRepository
	user *User
}

func (p qqPriceUserRepo) GetByID(context.Context, int64) (*User, error) { return p.user, nil }

type qqPriceRateRepo struct {
	UserGroupRateRepository
	rates map[int64]float64
	err   error
}

func (p qqPriceRateRepo) GetByUserID(context.Context, int64) (map[int64]float64, error) {
	return p.rates, p.err
}

type qqPriceChannelRepo struct{ ChannelRepository }

func (qqPriceChannelRepo) ListAll(context.Context) ([]Channel, error) { return nil, nil }
func (qqPriceChannelRepo) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{}, nil
}

func TestQQBotModelPriceVisibilityAndPersonalRate(t *testing.T) {
	price := 1e-6
	groups := qqPriceGroupRepo{groups: []Group{
		{ID: 1, Name: "public", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 2, ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-5"}, InputPrice: &price, OutputPrice: &price}}},
		{ID: 2, Name: "exclusive", Platform: PlatformOpenAI, Status: StatusActive, IsExclusive: true, RateMultiplier: 3},
		{ID: 3, Name: "disabled", Platform: PlatformOpenAI, Status: StatusDisabled, RateMultiplier: 4},
	}}
	billing := NewBillingService(&config.Config{}, nil)
	channels := NewChannelService(qqPriceChannelRepo{}, groups, nil, nil, nil)
	p := qqBotModelPrices{groups: groups, users: qqPriceUserRepo{user: &User{ID: 1, Status: StatusActive, RestrictPublicGroups: true, AllowedGroups: []int64{1}}}, rates: qqPriceRateRepo{rates: map[int64]float64{1: .5}}, channels: channels, billing: billing, resolver: NewModelPricingResolver(channels, billing)}
	blocks, err := p.ModelPrice(context.Background(), "gpt-5", 0, time.Now())
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "公开报价")
	require.Contains(t, blocks[0], "输入：2.000000")
	blocks, err = p.ModelPrice(context.Background(), "gpt-5", 1, time.Now())
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "本人报价")
	require.Contains(t, blocks[0], "输入：0.500000")
	blocks, err = p.ModelPrice(context.Background(), "made-up-claude-model", 0, time.Now())
	require.NoError(t, err)
	require.Empty(t, blocks)
	p.rates = qqPriceRateRepo{err: errors.New("unavailable")}
	_, err = p.ModelPrice(context.Background(), "gpt-5", 1, time.Now())
	require.Error(t, err, "never silently fall back from unknown personal rates")
}

func TestQQBotPriceQuotesUseBillingAndTierBoundaries(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	resolver := NewModelPricingResolver(nil, billing)
	p := qqBotModelPrices{billing: billing, resolver: resolver}
	in, out, read := 2e-6, 4e-6, .2e-6
	boundary := 100
	g := Group{ID: 1, LongContextPricingEnabled: true}
	r := &ResolvedPricing{Mode: BillingModeToken, Source: PricingSourceChannel, BasePricing: &ModelPricing{InputPricePerToken: 1e-6, OutputPricePerToken: 2e-6, CacheReadPricePerToken: .1e-6}, longContextPricingEnabled: true,
		Intervals:      []PricingInterval{{MinTokens: 0, MaxTokens: &boundary}, {MinTokens: 100, InputPrice: &in, OutputPrice: &out, CacheReadPrice: &read}},
		channelPricing: &ChannelModelPricing{TimePricing: &ChannelTimePricing{Timezone: "Asia/Shanghai", Periods: []ChannelTimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}}}},
	}
	at := time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)
	lines, err := p.quotePriceLines(context.Background(), &g, "custom", r, 3, at)
	require.NoError(t, err)
	require.Len(t, lines, 2)
	require.Contains(t, lines[0], "输入：6.000000")
	require.Contains(t, lines[1], "上下文 101 Token 起")
	require.Contains(t, lines[1], "输入：12.000000")
	require.Contains(t, lines[1], "输出：24.000000")
	require.Contains(t, lines[1], "缓存读取：1.200000")
	g.LongContextPricingEnabled = false
	r.longContextPricingEnabled = false
	lines, err = p.quotePriceLines(context.Background(), &g, "custom", r, 0, at)
	require.NoError(t, err)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], "输入：0.000000")
}

func TestQQBotPriceQuoteDeepSeekAndLongContext(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	resolver := NewModelPricingResolver(nil, billing)
	p := qqBotModelPrices{billing: billing, resolver: resolver}
	g := Group{ID: 1, LongContextPricingEnabled: true}
	r := resolver.Resolve(context.Background(), PricingInput{Model: "deepseek-v4-flash", Group: &g})
	peak := time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)
	lines, err := p.quotePriceLines(context.Background(), &g, "deepseek-v4-flash", r, 1, peak)
	require.NoError(t, err)
	require.Contains(t, strings.Join(lines, "\n"), "输入：0.300000")
	lines, err = p.quotePriceLines(context.Background(), &g, "deepseek-v4-flash", r, 1, peak.Add(12*time.Hour))
	require.NoError(t, err)
	require.Contains(t, strings.Join(lines, "\n"), "输入：0.150000")
	r = &ResolvedPricing{Mode: BillingModeToken, Source: PricingSourceChannel, BasePricing: &ModelPricing{InputPricePerToken: 1e-6, OutputPricePerToken: 2e-6, LongContextInputThreshold: 100, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 3}, longContextPricingEnabled: true}
	lines, err = p.quotePriceLines(context.Background(), &g, "custom", r, 1, peak)
	require.NoError(t, err)
	require.Len(t, lines, 2)
	require.Contains(t, lines[1], "输入：2.000000")
	require.Contains(t, lines[1], "输出：6.000000")
}
