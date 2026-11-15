package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// siteQuery contains only public query arguments. Account identity always comes
// from the QQ job and is never accepted from model-generated arguments.
type siteQuery struct {
	Kind     string `json:"kind"`
	Model    string `json:"model"`
	Activity string `json:"activity"`
	Page     int    `json:"page"`
}

func validateSiteQuery(q siteQuery) error { return q.validate() }

func (q siteQuery) validate() error {
	if q.Page < 0 || q.Page > 10000 {
		return errors.New("查询页码须为 1 到 10000 的整数")
	}
	switch q.Kind {
	case "model_price":
		if q.Activity != "" {
			return errors.New("价格查询不能指定活动类型")
		}
		if q.Model == "" || len(q.Model) > 200 || strings.IndexFunc(q.Model, unicode.IsSpace) >= 0 || strings.IndexFunc(q.Model, unicode.IsControl) >= 0 {
			return errors.New("请提供准确的模型名称，最多 200 字节且不能包含空白")
		}
	case "channel_status":
		if q.Model != "" || q.Activity != "" {
			return errors.New("渠道状态查询不接受模型或活动参数")
		}
	case "account_balance", "today_usage", "lottery_chances":
		if q.Model != "" || q.Activity != "" || q.Page > 1 {
			return errors.New("本人账户查询不接受模型或活动参数，页码须为1")
		}
	case "activity_rules":
		if q.Model != "" {
			return errors.New("活动规则查询不能指定模型")
		}
		switch q.Activity {
		case "", "all", "checkin", "invite", "lottery", "growth":
		default:
			return errors.New("活动类型仅支持签到、邀请、抽奖、成长任务或全部规则")
		}
	default:
		return errors.New("仅支持模型价格、渠道状态、活动规则、本人余额、今日用量和抽奖次数查询")
	}
	return nil
}

// SiteQueryDecision contains safe metadata, separate from delivery status.
type SiteQueryDecision struct {
	Kind       string `json:"kind"`
	Model      string `json:"model"`
	Activity   string `json:"activity"`
	Page       int    `json:"page"`
	Status     string `json:"status"`
	ErrorCode  string `json:"error_code,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}
type SiteQueryResult struct {
	Text     string            `json:"text"`
	Decision SiteQueryDecision `json:"decision"`
}

func siteQueryOutcome(text, status, code string) SiteQueryResult {
	return SiteQueryResult{Text: text, Decision: SiteQueryDecision{Status: status, ErrorCode: code}}
}
func (s *Service) querySiteInfo(ctx context.Context, c Config, j Job, q siteQuery) (string, error) {
	result, err := s.querySiteInfoDetailed(ctx, c, j, q)
	return result.Text, err
}
func (s *Service) querySiteInfoDetailed(ctx context.Context, c Config, j Job, q siteQuery) (SiteQueryResult, error) {
	if err := q.validate(); err != nil {
		return SiteQueryResult{}, err
	}
	if q.Page == 0 {
		q.Page = 1
	}
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	now := time.Now()
	var result SiteQueryResult
	switch q.Kind {
	case "model_price":
		result = s.querySiteModelPrice(queryCtx, c, j, q, now)
	case "channel_status":
		result = s.querySiteChannelStatus(queryCtx, q)
	case "activity_rules":
		result = s.querySiteActivityRules(queryCtx, c, j, q)
	case "account_balance", "today_usage", "lottery_chances":
		result = s.querySiteAccount(queryCtx, c, j, q, now)
	}
	result.Decision.Kind, result.Decision.Model, result.Decision.Activity, result.Decision.Page = q.Kind, q.Model, q.Activity, q.Page
	result.Decision.DurationMS = time.Since(now).Milliseconds()
	result.Text += "\n\n查询时间：" + time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02 15:04:05（北京时间）")
	return result, nil
}

func (s *Service) querySiteModelPrice(ctx context.Context, c Config, j Job, q siteQuery, at time.Time) SiteQueryResult {
	if s.modelPrices == nil || s.db == nil {
		return siteQueryOutcome("价格查询暂时不可用。", "unavailable", "provider_unavailable")
	}
	uid, err := boundUser(ctx, s.db, c.AppID, j.MemberID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return siteQueryOutcome("价格查询失败，请稍后重试。", "failed", "binding_lookup_failed")
	}
	blocks, err := s.modelPrices.ModelPrice(ctx, q.Model, uid, at)
	if err != nil {
		return siteQueryOutcome("价格查询失败，请稍后重试。", "failed", "provider_failed")
	}
	if len(blocks) == 0 {
		return siteQueryOutcome("未找到该模型价格，请核对名称或查看网站。", "not_found", "model_not_found")
	}
	return siteQueryPageResult(blocks, q.Page, "价格 "+replyPlainLabel(q.Model))
}

func (s *Service) querySiteChannelStatus(ctx context.Context, q siteQuery) SiteQueryResult {
	if s.channelStatus == nil {
		return siteQueryOutcome("渠道状态暂时不可用，请稍后重试。", "unavailable", "provider_unavailable")
	}
	report, err := s.channelStatus.ChannelStatus(ctx)
	if err != nil || report == nil {
		switch {
		case errors.Is(err, ErrChannelStatusDisabled):
			return siteQueryOutcome("渠道监控未开启。", "unavailable", "monitor_disabled")
		case errors.Is(err, ErrChannelStatusUnsupported):
			return siteQueryOutcome("被动监控未开启。", "unavailable", "monitor_unsupported")
		default:
			return siteQueryOutcome("渠道状态查询失败，请稍后重试。", "failed", "provider_failed")
		}
	}
	return siteQueryPageResult(formatChannelStatus(report), q.Page, "渠道状态")
}
func siteQueryPageResult(blocks []string, page int, command string) SiteQueryResult {
	status, code := "succeeded", ""
	if len(blocks) == 0 {
		status, code = "not_found", "no_data"
	} else if page < 0 || page > (len(blocks)+4)/5 {
		status, code = "not_found", "page_out_of_range"
	}
	return siteQueryOutcome(siteQueryPage(blocks, page, command), status, code)
}

// Keep complete provider blocks and reuse the command's five-block page size.
// Large results remain explicitly accessible instead of truncating price tiers.
func siteQueryPage(blocks []string, page int, command string) string {
	if len(blocks) == 0 {
		return "暂无内容。"
	}
	if page == 0 {
		page = 1
	}
	pages := (len(blocks) + 4) / 5
	if page < 1 || page > pages {
		return fmt.Sprintf("查询结果共 %d 页，请指定有效页码。", pages)
	}
	body := strings.Join(blocks[(page-1)*5:min(page*5, len(blocks))], "\n\n")
	if utf8.RuneCountInString(body) > 5000 {
		if command == "" {
			return "查询结果较长，请缩小范围到具体活动后再查询。"
		}
		if page > 1 {
			command += fmt.Sprintf(" %d", page)
		}
		return "查询结果较长，请通过 " + command + " 查看完整内容。"
	}
	if pages > 1 {
		body += fmt.Sprintf("\n\n第 %d/%d 页", page, pages)
		if page < pages {
			body += fmt.Sprintf("\n可继续查询第 %d 页", page+1)
			if command != "" {
				body += fmt.Sprintf("；指令：%s %d", command, page+1)
			}
		}
	}
	return body
}

func (s *Service) querySiteActivityRules(ctx context.Context, c Config, j Job, q siteQuery) SiteQueryResult {
	if s.db == nil {
		return siteQueryOutcome("活动规则查询暂时不可用。", "unavailable", "storage_unavailable")
	}
	// Read only configuration, with no user progress or settlement helpers.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return siteQueryOutcome("活动规则查询失败，请稍后重试。", "failed", "config_read_failed")
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.loadConfig(ctx, tx)
	if err != nil {
		return siteQueryOutcome("活动规则查询失败，请稍后重试。", "failed", "config_read_failed")
	}
	if current.AppID != c.AppID || !current.Enabled || !current.allows(j.GroupID) {
		return siteQueryOutcome("当前群未启用机器人，活动规则暂时不可用。", "unavailable", "group_disabled")
	}
	if current.groupPolicy(j.GroupID).DisableActivities {
		return siteQueryOutcome("当前群未开启活动。", "unavailable", "activities_disabled")
	}
	activity := q.Activity
	if activity == "" {
		activity = "all"
	}
	var sections []string
	if activity == "all" || activity == "checkin" || activity == "growth" {
		checkin, e := loadCheckinConfig(ctx, tx, current.CheckinReward)
		if e != nil {
			return siteQueryOutcome("活动规则查询失败，请稍后重试。", "failed", "config_read_failed")
		}
		if activity == "all" || activity == "checkin" {
			sections = append(sections, siteCheckinRules(checkin))
		}
		if activity == "all" || activity == "growth" {
			sections = append(sections, siteGrowthRules(checkin.Growth))
		}
	}
	if activity == "all" || activity == "invite" {
		sections = append(sections, siteInviteRules(current))
	}
	if activity == "all" || activity == "lottery" {
		lottery, e := loadLotteryConfig(ctx, tx)
		if e != nil {
			return siteQueryOutcome("活动规则查询失败，请稍后重试。", "failed", "config_read_failed")
		}
		sections = append(sections, siteLotteryRules(lottery))
	}
	if err = tx.Commit(); err != nil {
		return siteQueryOutcome("活动规则查询失败，请稍后重试。", "failed", "config_read_failed")
	}
	body := strings.Join(sections, "\n\n") + "\n\n以上仅为当前活动规则；未查询个人资格或进度，未执行签到、抽奖或发奖。"
	return siteQueryPageResult(paginateCommand(body), q.Page, "")
}

func siteRuleMoney(amount float64) string {
	return "USD " + decimal.NewFromFloat(amount).String()
}

func lotteryPrizeDescription(prize LotteryPrize) string {
	parts := []string{}
	if prize.Amount > 0 {
		parts = append(parts, siteRuleMoney(prize.Amount))
	}
	if prize.Chances > 0 {
		parts = append(parts, fmt.Sprintf("抽奖次数 %d 次", prize.Chances))
	}
	if prize.CouponQuantity > 0 {
		parts = append(parts, fmt.Sprintf("充值优惠券 × %d", prize.CouponQuantity))
	}
	if len(parts) == 0 {
		return siteRuleMoney(0)
	}
	return strings.Join(parts, " + ")
}

func siteCheckinRules(c CheckinConfig) string {
	if !c.Enabled {
		return "📅 **签到规则**\n签到活动未开启。"
	}
	amount := siteRuleMoney(c.MinAmount)
	if c.MinAmount != c.MaxAmount {
		amount += "～" + siteRuleMoney(c.MaxAmount)
	}
	if c.CouponQuantity > 0 {
		amount += fmt.Sprintf(" + 充值优惠券 × %d", c.CouponQuantity)
	}
	text := "📅 **签到规则**\n每日签到一次（北京时间）\n奖励：" + amount + "\n充值条件：" + c.describe() + "\n参与：签到"
	if reward := c.streakReward(); reward.enabled() {
		text += fmt.Sprintf("\n连续签到每 %d 天奖励：%s", c.streakDays(), reward.description())
		text += siteRuleClaimInstructions(reward)
	}
	for _, milestone := range c.Milestones {
		if !milestone.Enabled || !milestone.Reward.enabled() {
			continue
		}
		mode := "累计签到"
		if milestone.kind() == "checkin_streak" {
			mode = "连续签到"
		}
		frequency := "每账号限一次"
		if milestone.Repeat {
			mode, frequency = "每累计签到", "达标可重复获得"
		}
		text += fmt.Sprintf("\n\n%s：%s %d 天\n奖励：%s（%s）", replyPlainLabel(milestone.Name), mode, milestone.Days, milestone.Reward.description(), frequency)
		text += siteRuleClaimInstructions(milestone.Reward)
	}
	return text
}

func siteInviteRules(c Config) string {
	if !c.InviteEnabled || !c.inviteHasReward() || c.InviteDailyLimit <= 0 {
		return "🎁 **邀请规则**\n邀请活动未开启。"
	}
	return fmt.Sprintf("🎁 **邀请规则**\n每位有效受邀者奖励：%s\n每位邀请人每日上限：%d 人（北京时间）\n通过官方邀请，7 天内入群并留群；双方绑定账号，每位受邀账号限奖一次。\n%s\n查询：邀请奖励", c.inviteRewardDescription(), c.InviteDailyLimit, c.inviteRechargeRequirement().invite())
}

func siteLotteryRules(c LotteryConfig) string {
	if !c.Enabled {
		return "🎉 **抽奖规则**\n抽奖活动未开启。"
	}
	text := fmt.Sprintf("🎉 **%s**\n累计有效充值每满 %s 送 %d 次\n每次抽奖消耗 1 次；可发送 抽奖 1～10 连抽\n需绑定网站账号。\n\n**普通奖池**", replyPlainLabel(c.Title), siteRuleMoney(c.RechargeStep), c.ChancesPerStep)
	for _, p := range c.Prizes {
		text += fmt.Sprintf("\n\n%s\n%s（概率 %.2f%%）", replyPlainLabel(p.Name), lotteryPrizeDescription(p), float64(p.ProbabilityBPS)/100)
	}
	if c.GuaranteeEnabled {
		text += fmt.Sprintf("\n\n**保底奖池**\n累计每抽 %d 次触发（含历史）；普通中奖不重置保底进度。", c.GuaranteeEvery)
		for _, p := range c.GuaranteePrizes {
			text += fmt.Sprintf("\n\n%s\n%s（概率 %.2f%%）", replyPlainLabel(p.Name), lotteryPrizeDescription(p), float64(p.ProbabilityBPS)/100)
		}
	}
	return text
}

func siteGrowthRules(c GrowthConfig) string {
	text := "🌱 **成长任务规则**"
	count := 0
	for _, set := range []struct {
		tasks    []GrowthTask
		periodic bool
	}{{c.tasks(), false}, {c.PeriodicTasks, true}} {
		for _, task := range set.tasks {
			if !task.Enabled || !task.reward().enabled() {
				continue
			}
			count++
			kind := "一次性"
			switch growthTaskPeriod(task) {
			case "daily":
				kind = "每日挑战，仅统计本日记录，北京时间每日 00:00 重置"
			case "weekly":
				kind = "每周挑战，仅统计本周记录，北京时间周一 00:00 重置"
			}
			if set.periodic {
				kind = "周期任务，每累计达标可再获奖励"
				if isGrowthAmountDayKind(task.Kind) {
					kind = "周期任务，每轮达标后可再获奖励，已结算日期不重复使用"
					if isGrowthAmountStreakKind(task.Kind) {
						kind += "，未达标的连续进度遇间断重新计算"
					}
				}
			}
			text += fmt.Sprintf("\n\n**%s**\n类型：%s\n目标：%s\n奖励：%s", replyPlainLabel(task.Name), kind, siteGrowthTarget(task), task.reward().description())
			text += siteRuleClaimInstructions(task.reward())
		}
	}
	if count == 0 {
		return text + "\n暂无已开启的成长任务。"
	}
	return text + "\n\n需绑定网站账号；达标自动发放，实物及自定义奖品由管理员交付。\n个人进度：成长任务"
}

func siteGrowthTarget(t GrowthTask) string {
	switch t.Kind {
	case "binding":
		return "完成账号绑定"
	case "usage_count":
		return fmt.Sprintf("成功 API 调用累计 %.0f 次", t.Target)
	case "usage_days":
		return fmt.Sprintf("完成 API 调用的活跃日期达到 %.0f 天", t.Target)
	case "model_count":
		return fmt.Sprintf("探索 %.0f 种不同模型", t.Target)
	case "invite_count":
		return fmt.Sprintf("已结算的有效邀请达到 %.0f 人", t.Target)
	case "checkin_total":
		return fmt.Sprintf("累计签到 %.0f 天", t.Target)
	case "checkin_streak":
		return fmt.Sprintf("连续签到 %.0f 天", t.Target)
	case "usage_tokens":
		return fmt.Sprintf("累计消耗 %.0f Token", t.Target)
	case "usage_amount":
		return "累计消费 " + siteRuleMoney(t.Target)
	case "usage_amount_days":
		return fmt.Sprintf("累计 %.0f 天每日消费满 %s（按北京时间日期统计）", t.Target, siteRuleMoney(t.DailyAmount))
	case "usage_amount_streak":
		return fmt.Sprintf("连续 %.0f 天每日消费满 %s（按北京时间日期统计）", t.Target, siteRuleMoney(t.DailyAmount))
	case "recharge_days":
		return fmt.Sprintf("累计 %.0f 天每日有效充值满 %s（按北京时间日期统计）", t.Target, siteRuleMoney(t.DailyAmount))
	case "recharge_streak":
		return fmt.Sprintf("连续 %.0f 天每日有效充值满 %s（按北京时间日期统计）", t.Target, siteRuleMoney(t.DailyAmount))
	case "recharge_total":
		return "累计有效充值 " + siteRuleMoney(t.Target)
	default:
		return "任务类型暂不支持展示，请查看网站规则。"
	}
}

func siteRuleClaimInstructions(r GrowthReward) string {
	if r.Type != "physical" && r.Type != "other" {
		return ""
	}
	if strings.TrimSpace(r.ClaimInstructions) == "" {
		return "\n领取：联系管理员。"
	}
	return "\n领取：" + replyPlainLabel(r.ClaimInstructions)
}
