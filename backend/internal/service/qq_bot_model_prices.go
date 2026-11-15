package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

type qqBotModelPrices struct {
	groups   GroupRepository
	users    UserRepository
	rates    UserGroupRateRepository
	channels *ChannelService
	billing  *BillingService
	resolver *ModelPricingResolver
}

func qqPriceLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "~", `\~`, "[", `\[`, "]", `\]`, "<", `\<`, ">", `\>`, "$", `\$`).Replace(strings.Join(strings.Fields(s), " "))
}

func (p *qqBotModelPrices) ModelPrice(ctx context.Context, model string, uid int64, at time.Time) ([]string, error) {
	groups, err := p.groups.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	var user *User
	rates := map[int64]float64{}
	if uid > 0 {
		user, err = p.users.GetByID(ctx, uid)
		if err != nil {
			return nil, err
		}
		if user == nil || !user.IsActive() || user.DeletedAt != nil {
			return nil, ErrUserNotFound
		}
		rates, err = p.rates.GetByUserID(ctx, uid)
		if err != nil {
			return nil, err
		}
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	var blocks []string
	for _, g := range groups {
		if !g.IsActive() || !g.ModelAllowlist.Allows(model) {
			continue
		}
		if user == nil {
			if g.IsExclusive {
				continue
			}
		} else if !user.CanBindGroup(g.ID, g.IsExclusive) {
			continue
		}
		lookup, err := p.channels.lookupGroupChannel(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		billingModel := model
		note := ""
		configured := matchGroupModelPricing(&g, model) != nil
		if lookup != nil {
			mapping := resolveMapping(lookup, g.ID, model)
			if mapping.BillingModelSource == BillingModelSourceChannelMapped && mapping.Mapped {
				billingModel = mapping.MappedModel
			}
			if mapping.BillingModelSource == BillingModelSourceUpstream || mapping.BillingModelSource == BillingModelSourceResponse {
				note = "\n参考价：渠道按最终模型计费。"
			}
			if checkRestricted(lookup, g.ID, billingModel) {
				continue
			}
			configured = configured || p.resolver.lookupChannelPricingNormalized(ctx, g.ID, billingModel) != nil || mapping.Mapped
		}
		if !configured {
			platform, known := DetectModelPlatform(model)
			if !known || (g.Platform != platform && g.Platform != PlatformAntigravity) {
				continue
			}
			if !p.billing.HasIdentifiedTokenPricing(billingModel) {
				continue
			}
		}
		resolved := p.resolver.Resolve(ctx, PricingInput{Model: billingModel, GroupID: &g.ID, Group: &g})
		if resolved.Source != PricingSourceGroup && resolved.Source != PricingSourceChannel && !p.billing.HasIdentifiedTokenPricing(billingModel) {
			continue
		}
		if resolved.BasePricing == nil && resolved.Mode == BillingModeToken {
			continue
		}
		rate := g.RateMultiplier
		kind := "公开报价"
		if user != nil {
			kind = "本人报价"
			if custom, ok := rates[g.ID]; ok {
				rate = custom
			}
		}
		if resolved.Mode == BillingModeToken || resolved.Mode == BillingModePerRequest {
			rate *= g.PeakMultiplierAt(at)
		} else if resolved.Mode == BillingModeImage && g.ImageRateIndependent {
			rate = g.ImageRateMultiplier
		} else if resolved.Mode == BillingModeVideo && g.VideoRateIndependent {
			rate = g.VideoRateMultiplier
		}
		header := fmt.Sprintf("💰 **%s**\n模型：%s\n分组：%s\n倍率：%g×", kind, qqPriceLabel(model), qqPriceLabel(g.Name), rate)
		if billingModel != model {
			header += "\n计费模型：" + qqPriceLabel(billingModel)
		}
		if g.IsSubscriptionType() {
			header += "\n使用需有效订阅。"
		}
		lines, err := p.quotePriceLines(ctx, &g, billingModel, resolved, rate, at)
		if err != nil {
			return nil, err
		}
		footer := "\n\n标准服务层级、默认推理等级\n图片/音频及工具费另计\n更新：" + at.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("01-02 15:04（北京时间）") + note
		chunk := header
		for _, line := range lines {
			if len([]rune(chunk+line+footer)) > 1450 && chunk != header {
				blocks = append(blocks, chunk+footer)
				chunk = header + "（续）"
			}
			chunk += "\n\n" + line
		}
		blocks = append(blocks, chunk+footer)
	}
	return blocks, nil
}

func (p *qqBotModelPrices) quotePriceLines(ctx context.Context, g *Group, model string, r *ResolvedPricing, rate float64, at time.Time) ([]string, error) {
	base := CostInput{Ctx: ctx, Model: model, Group: g, GroupID: &g.ID, RateMultiplier: rate, PricingAt: at, Resolver: p.resolver, Resolved: r}
	if r.Mode != BillingModeToken {
		base.RequestCount = 1
		cost, err := p.billing.CalculateCostUnified(base)
		if err != nil {
			return nil, err
		}
		lines := []string{fmt.Sprintf("默认：USD %.6f / 次", cost.ActualCost)}
		for _, tier := range r.RequestTiers {
			q := base
			q.SizeTier = tier.TierLabel
			q.Tokens.InputTokens = max(1, tier.MinTokens+1)
			cost, err = p.billing.CalculateCostUnified(q)
			if err != nil {
				return nil, err
			}
			label := qqPriceLabel(tier.TierLabel)
			if label == "" {
				label = fmt.Sprintf("上下文 %d Token 起", tier.MinTokens+1)
			}
			lines = append(lines, fmt.Sprintf("%s：USD %.6f / 次", label, cost.ActualCost))
		}
		return lines, nil
	}
	// Evaluate the billing function at tier boundaries, with one token in each
	// component. Context size only selects the tier; it never creates a paid call.
	points := []int{1}
	if g.LongContextPricingEnabled {
		for _, iv := range r.Intervals {
			points = append(points, max(1, iv.MinTokens+1))
			if iv.MaxTokens != nil {
				points = append(points, *iv.MaxTokens+1)
			}
		}
		if len(r.Intervals) == 0 && r.BasePricing != nil && r.BasePricing.LongContextInputThreshold > 0 {
			n := r.BasePricing.LongContextInputThreshold + 1
			if r.BasePricing.LongContextThresholdInclusive {
				n--
			}
			points = append(points, n)
		}
	}
	sort.Ints(points)
	var lines []string
	for i, n := range points {
		if i > 0 && n == points[i-1] {
			continue
		}
		values := make([]float64, 4)
		for component := range values {
			q := base
			switch component {
			case 0:
				q.Tokens.InputTokens = n
			case 1:
				q.Tokens.InputTokens = n
				q.Tokens.OutputTokens = 1
			case 2:
				q.Tokens.InputTokens = n - 1
				q.Tokens.CacheReadTokens = 1
			case 3:
				q.Tokens.InputTokens = n - 1
				q.Tokens.CacheCreationTokens = 1
			}
			cost, err := p.billing.CalculateCostUnified(q)
			if err != nil {
				return nil, err
			}
			switch component {
			case 0:
				values[component] = cost.InputCost / float64(n)
			case 1:
				values[component] = cost.OutputCost
			case 2:
				values[component] = cost.CacheReadCost
			case 3:
				values[component] = cost.CacheCreationCost
			}
			values[component] *= rate * 1e6
		}
		label := "基础档"
		if n > 1 {
			label = fmt.Sprintf("上下文 %d Token 起", n)
		}
		lines = append(lines, fmt.Sprintf("**%s**\n单位：USD / 百万 Token\n输入：%.6f\n输出：%.6f\n缓存读取：%.6f\n缓存写入（默认时长）：%.6f", label, values[0], values[1], values[2], values[3]))
	}
	return lines, nil
}
