package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
)

type qqBotPriceSource struct {
	channels interface {
		ListAll(context.Context) ([]Channel, error)
	}
	groups interface {
		List(context.Context, pagination.PaginationParams) ([]Group, *pagination.PaginationResult, error)
	}
}

func (p *qqBotPriceSource) listGroups(ctx context.Context) ([]Group, error) {
	var groups []Group
	var total int64
	for page := 1; ; page++ {
		batch, result, err := p.groups.List(ctx, pagination.PaginationParams{Page: page, PageSize: 1000, SortBy: "id", SortOrder: pagination.SortOrderAsc})
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, fmt.Errorf("missing group pagination")
		}
		if page == 1 {
			total = result.Total
		} else if result.Total != total {
			return nil, fmt.Errorf("group list changed during scan")
		}
		groups = append(groups, batch...)
		if int64(len(groups)) == total {
			return groups, nil
		}
		if len(batch) == 0 || int64(len(groups)) > total {
			return nil, fmt.Errorf("incomplete group list")
		}
	}
}

func (p *qqBotPriceSource) PriceSnapshot(ctx context.Context, now time.Time) (qqbot.PriceSnapshot, error) {
	out := qqbot.PriceSnapshot{Version: 1, States: []qqbot.PriceState{}, NextCheck: now.Add(15 * time.Second)}
	channels, err := p.channels.ListAll(ctx)
	if err != nil {
		return out, err
	}
	groups, err := p.listGroups(ctx)
	if err != nil {
		return out, err
	}
	for _, ch := range channels {
		state := qqbot.PriceState{Key: fmt.Sprintf("channel:%d", ch.ID), Kind: "channel", Name: ch.Name, Status: ch.Status}
		// IDs and timestamps are deliberately excluded: channel saves replace pricing
		// rows even when only a name or description changed.
		for _, pricing := range ch.ModelPricing {
			for _, model := range pricing.Models {
				key := pricing.Platform + "/" + normalizeChannelPricingModelName(model) + "/"
				subject := model + "（" + pricing.Platform + "）"
				fields := qqPriceFields(pricing, now)
				for _, f := range fields {
					f.Key = key + f.Key
					f.Model = subject
					state.Fields = append(state.Fields, f)
				}
			}
			if ch.IsActive() && pricing.TimePricing != nil && validateChannelTimePricing(pricing.TimePricing) == nil {
				loc, err := loadChannelTimePricingLocation(pricing.TimePricing.Timezone)
				if err == nil {
					for _, period := range pricing.TimePricing.Periods {
						out.NextCheck = qqNextBoundary(now, out.NextCheck, loc, period.StartTime, period.EndTime)
					}
					// Weekday eligibility changes at local midnight, including full-day rules.
					out.NextCheck = qqNextBoundary(now, out.NextCheck, loc, "00:00")
				}
			}
		}
		out.States = append(out.States, state)
	}
	for _, g := range groups {
		state := qqbot.PriceState{Key: fmt.Sprintf("group:%d", g.ID), Kind: "group", Name: g.Name, Status: g.Status}
		state.Fields = []qqbot.PriceField{
			{Key: "01_text", Label: "倍率", Value: qqMultiplier(g.RateMultiplier)},
			{Key: "02_image", Label: "图片倍率", Value: qqIndependentRate(g.ImageRateIndependent, g.ImageRateMultiplier)},
			{Key: "03_video", Label: "视频倍率", Value: qqIndependentRate(g.VideoRateIndependent, g.VideoRateMultiplier)},
			{Key: "04_peak", Label: "高峰规则", Value: qqPeakRule(g)},
			{Key: "05_current", Label: "倍率", Value: qqMultiplier(g.RateMultiplier * g.PeakMultiplierAt(now)), Scheduled: true},
		}
		for _, pricing := range g.ModelPricing {
			fields := qqPriceFields(pricing, now)
			for _, model := range pricing.Models {
				platform := pricing.Platform
				if platform == "" {
					platform = g.Platform
				}
				key := "model/" + platform + "/" + normalizeChannelPricingModelName(model) + "/"
				subject := model
				if platform != "" {
					subject += "（" + platform + "）"
				}
				for _, field := range fields {
					field.Key = key + field.Key
					field.Model = subject
					state.Fields = append(state.Fields, field)
				}
			}
		}
		if g.IsActive() && g.IsSubscriptionType() && g.PeakRateEnabled {
			out.NextCheck = qqNextBoundary(now, out.NextCheck, timezone.Location(), g.PeakStart, g.PeakEnd)
		}
		out.States = append(out.States, state)
	}
	return out, nil
}

func qqNumber(n float64) string     { return strconv.FormatFloat(n, 'f', -1, 64) }
func qqMultiplier(n float64) string { return qqNumber(n) + "×" }
func qqIndependentRate(independent bool, n float64) string {
	if !independent {
		return "跟随基础倍率"
	}
	return qqMultiplier(n)
}
func qqPeakRule(g Group) string {
	if !g.IsSubscriptionType() || !g.PeakRateEnabled {
		return "关闭"
	}
	return g.PeakStart + "–" + g.PeakEnd + "，" + qqMultiplier(g.PeakRateMultiplier) + "（" + timezone.Location().String() + "）"
}

func qqNextBoundary(now, next time.Time, loc *time.Location, clocks ...string) time.Time {
	local := now.In(loc)
	for _, clock := range clocks {
		parts := strings.Split(clock, ":")
		if len(parts) < 2 || len(parts) > 3 {
			continue
		}
		h, e1 := strconv.Atoi(parts[0])
		m, e2 := strconv.Atoi(parts[1])
		sec := 0
		if len(parts) == 3 {
			var err error
			sec, err = strconv.Atoi(parts[2])
			if err != nil {
				continue
			}
		}
		if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 || sec < 0 || sec > 59 {
			continue
		}
		for day := 0; day <= 1; day++ {
			boundary := time.Date(local.Year(), local.Month(), local.Day()+day, h, m, sec, 0, loc)
			if boundary.After(now) && boundary.Before(next) {
				next = boundary
			}
		}
	}
	return next
}

func qqPriceFields(p ChannelModelPricing, now time.Time) []qqbot.PriceField {
	mode := p.BillingMode
	if mode == "" {
		mode = BillingModeToken
	}
	label := map[BillingMode]string{BillingModeToken: "按 Token", BillingModePerRequest: "按次", BillingModeImage: "按图片", BillingModeVideo: "按视频"}[mode]
	fields := []qqbot.PriceField{{Key: "00_mode", Label: "计费方式", Value: label}}
	addPrice := func(key, label string, value *float64, unit string) {
		v := "未设置（沿用计费默认值）"
		if value != nil {
			n := *value
			if unit == "百万 Token" {
				n *= 1e6
			}
			// Stored prices are USD/token. Keep the human-facing conversion free of
			// binary float noise while retaining ten decimal places per million.
			v = "USD " + strings.TrimRight(strings.TrimRight(strconv.FormatFloat(n, 'f', 10, 64), "0"), ".")
		}
		fields = append(fields, qqbot.PriceField{Key: key, Label: label, Value: v, Unit: unit})
	}
	addMultiplier := func(key, label string, value *float64) {
		v := "默认"
		if value != nil {
			v = qqMultiplier(*value)
		}
		fields = append(fields, qqbot.PriceField{Key: key, Label: label, Value: v})
	}
	addPrice("01_input", "输入", p.InputPrice, "百万 Token")
	addPrice("02_output", "输出", p.OutputPrice, "百万 Token")
	addPrice("03_cache_write", "缓存写入", p.CacheWritePrice, "百万 Token")
	addPrice("04_cache_write_1h", "缓存写入（1 小时）", p.CacheWrite1hPrice, "百万 Token")
	addPrice("05_cache_read", "缓存读取", p.CacheReadPrice, "百万 Token")
	addPrice("06_image_input", "图片输入", p.ImageInputPrice, "百万 Token")
	addPrice("07_image_output", "图片输出", p.ImageOutputPrice, "百万 Token")
	addPrice("08_request", "按次价格", p.PerRequestPrice, "次")
	addMultiplier("09_fast", "Fast 倍率", p.FastMultiplier)
	addMultiplier("10_flex", "Flex 倍率", p.FlexMultiplier)
	for effort, n := range p.ReasoningEffortMultipliers {
		addMultiplier("11_reasoning/"+effort, "思考强度 "+effort+" 倍率", &n)
	}
	// Sort by semantic boundaries, never the replace-on-save database IDs.
	for _, iv := range p.Intervals {
		end := "不限"
		if iv.MaxTokens != nil {
			end = strconv.Itoa(*iv.MaxTokens)
		}
		tier := fmt.Sprintf("%d–%s Token", iv.MinTokens, end)
		if iv.TierLabel != "" {
			tier = iv.TierLabel
		}
		key := fmt.Sprintf("20_interval/%d/%s/%s/", iv.MinTokens, end, iv.TierLabel)
		prefix := "区间 " + tier + " · "
		addPrice(key+"01_input", prefix+"输入", iv.InputPrice, "百万 Token")
		addPrice(key+"02_output", prefix+"输出", iv.OutputPrice, "百万 Token")
		addPrice(key+"03_cache_write", prefix+"缓存写入", iv.CacheWritePrice, "百万 Token")
		addPrice(key+"04_cache_write_1h", prefix+"缓存写入（1 小时）", iv.CacheWrite1hPrice, "百万 Token")
		addPrice(key+"05_cache_read", prefix+"缓存读取", iv.CacheReadPrice, "百万 Token")
		addPrice(key+"06_request", prefix+"按次价格", iv.PerRequestPrice, "次")
		addMultiplier(key+"07_input_multiplier", prefix+"输入倍率", iv.InputMultiplier)
		addMultiplier(key+"08_output_multiplier", prefix+"输出倍率", iv.OutputMultiplier)
		addMultiplier(key+"09_cache_write_multiplier", prefix+"缓存写入倍率", iv.CacheWriteMultiplier)
		addMultiplier(key+"10_cache_read_multiplier", prefix+"缓存读取倍率", iv.CacheReadMultiplier)
	}
	rule := "关闭"
	if p.TimePricing != nil && len(p.TimePricing.Periods) > 0 {
		rule = p.TimePricing.Timezone
		if p.TimePricing.WeekdaysOnly {
			rule += "，仅工作日"
		} else {
			rule += "，每天"
		}
		periods := append([]ChannelTimePricingPeriod(nil), p.TimePricing.Periods...)
		sort.Slice(periods, func(i, j int) bool { return periods[i].StartTime < periods[j].StartTime })
		for _, period := range periods {
			fields = append(fields, qqbot.PriceField{Key: "30_period/" + period.StartTime + "/" + period.EndTime, Label: "分时 " + period.StartTime + "–" + period.EndTime, Value: qqMultiplier(period.Multiplier)})
		}
	}
	fields = append(fields, qqbot.PriceField{Key: "29_time_rule", Label: "分时规则", Value: rule}, qqbot.PriceField{Key: "31_time_current", Label: "当前分时倍率", Value: qqMultiplier(p.TimePricing.MultiplierAt(now)), Scheduled: true})
	return fields
}
