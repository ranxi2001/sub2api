package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

type PreviewTask struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Progress string `json:"progress"`
	Eligible bool   `json:"eligible"`
	Status   string `json:"status"`
	Reward   string `json:"reward"`
}
type ActivityPreview struct {
	UserID              int64         `json:"user_id"`
	Bound               bool          `json:"bound"`
	CheckinEligible     bool          `json:"checkin_eligible"`
	CheckinRule         string        `json:"checkin_rule"`
	CheckinDone         bool          `json:"checkin_done"`
	InviteEligible      bool          `json:"invite_eligible"`
	InviteRule          string        `json:"invite_rule"`
	Lottery             LotteryState  `json:"lottery"`
	Tasks               []PreviewTask `json:"tasks"`
	CheckinMilestones   []PreviewTask `json:"checkin_milestones"`
	CheckinTotal        int           `json:"checkin_total"`
	CheckinExpected     float64       `json:"checkin_expected"`
	LotteryExpected     float64       `json:"lottery_expected"`
	LotteryNextExpected float64       `json:"lottery_next_expected"`
}

func expectedPrize(prizes []LotteryPrize) float64 {
	total := decimal.Zero
	for _, p := range prizes {
		total = total.Add(decimal.NewFromFloat(p.Amount).Mul(decimal.NewFromInt(int64(p.ProbabilityBPS))).Div(decimal.NewFromInt(10000)))
	}
	v, _ := total.Float64()
	return v
}
func lotteryExpected(c LotteryConfig) float64 {
	normal := expectedPrize(c.Prizes)
	if !c.GuaranteeEnabled || c.GuaranteeEvery < 1 {
		return normal
	}
	return (normal*float64(c.GuaranteeEvery-1) + expectedPrize(c.GuaranteePrizes)) / float64(c.GuaranteeEvery)
}

// Preview runs in a read-only transaction and never settles rewards or consumes chances.
func (s *Service) PreviewActivities(ctx context.Context, uid int64) (ActivityPreview, error) {
	out := ActivityPreview{UserID: uid, Tasks: []PreviewTask{}, CheckinMilestones: []PreviewTask{}}
	if uid <= 0 {
		return out, errors.New("网站用户 ID 不正确")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := s.loadConfig(ctx, tx)
	if err != nil {
		return out, err
	}
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND status='active' AND deleted_at IS NULL)`, uid).Scan(&active)
	if err != nil {
		return out, err
	}
	if !active {
		return out, errors.New("用户不存在或已停用")
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qq_bot_bindings WHERE app_id=$1 AND user_id=$2)`, c.AppID, uid).Scan(&out.Bound)
	if err != nil {
		return out, err
	}
	checkin, err := loadCheckinConfig(ctx, tx, c.CheckinReward)
	if err != nil {
		return out, err
	}
	lottery, err := loadLotteryConfig(ctx, tx)
	if err != nil {
		return out, err
	}
	eligible, err := checkin.eligibility(ctx, tx, uid, false)
	if err != nil {
		return out, err
	}
	out.CheckinRule = checkin.describe()
	if checkin.CouponQuantity > 0 {
		out.CheckinRule += fmt.Sprintf("；每次签到发放充值优惠券 × %d", checkin.CouponQuantity)
	}
	out.CheckinEligible = out.Bound && c.Enabled && checkin.Enabled && checkin.satisfied(eligible)
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qq_bot_rewards WHERE app_id=$1 AND user_id=$2 AND kind='checkin' AND (created_at AT TIME ZONE 'Asia/Shanghai')::date=$3::date)`, c.AppID, uid, day(time.Now())).Scan(&out.CheckinDone)
	if err != nil {
		return out, err
	}
	invite := c.inviteRechargeRequirement()
	invEligibility, err := invite.eligibility(ctx, tx, uid, false)
	if err != nil {
		return out, err
	}
	out.InviteRule = invite.describe() + "；每位有效受邀者奖励：" + c.inviteRewardDescription()
	out.InviteEligible = out.Bound && c.Enabled && c.InviteEnabled && c.inviteHasReward() && c.InviteDailyLimit > 0 && invite.satisfied(invEligibility)
	out.CheckinExpected = checkinExpectedAmount(checkin)
	out.LotteryExpected = lotteryExpected(lottery)
	out.Lottery, err = lotteryState(ctx, tx, lottery, uid, false)
	if err != nil {
		return out, err
	}
	out.LotteryNextExpected = expectedPrize(lottery.Prizes)
	if out.Lottery.DrawsUntilGuarantee == 1 {
		out.LotteryNextExpected = expectedPrize(lottery.GuaranteePrizes)
	}
	at := time.Now()
	total, streak, err := checkinProgress(ctx, tx, uid, at)
	if err != nil {
		return out, err
	}
	out.CheckinTotal = total
	allTasks := append(append([]GrowthTask{}, checkin.Growth.tasks()...), checkin.Growth.PeriodicTasks...)
	progressByPeriod, err := loadGrowthProgress(ctx, tx, uid, allTasks, at, false)
	if err != nil {
		return out, err
	}
	for _, task := range checkin.Growth.tasks() {
		progress := progressByPeriod[growthTaskPeriod(task)]
		ok, description := progress.task(task)
		snapshot, status, e := activityRewardSnapshot(ctx, tx, growthTaskRewardKey(uid, task, at))
		if e != nil {
			return out, e
		}
		reward := task.reward()
		if status != "" {
			reward = snapshot
		}
		out.Tasks = append(out.Tasks, PreviewTask{ID: task.ID, Name: task.Name, Progress: description, Eligible: out.Bound && c.Enabled && ok && task.Enabled && reward.enabled() && status == "", Status: status, Reward: reward.description()})
	}
	progress := progressByPeriod["once"]
	for _, task := range checkin.Growth.PeriodicTasks {
		if isGrowthAmountDayKind(task.Kind) {
			var cursor time.Time
			var cycles int64
			err = tx.QueryRowContext(ctx, `SELECT last_reward_day,cycles FROM qq_bot_growth_amount_day_progress WHERE user_id=$1 AND task_id=$2 AND kind=$3`, uid, task.ID, task.Kind).Scan(&cursor, &cycles)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
			ok, description := previewPeriodicAmountDays(task, progress, cursor, at)
			reward := task.reward()
			status := fmt.Sprintf("已结算 %d 轮", cycles)
			if ok {
				status += "；待结算"
			}
			out.Tasks = append(out.Tasks, PreviewTask{ID: "periodic:" + task.ID, Name: task.Name, Progress: description, Eligible: out.Bound && c.Enabled && task.Enabled && reward.enabled() && ok, Status: status, Reward: reward.description()})
			continue
		}
		consumed, cycles := decimal.Zero, int64(0)
		err = tx.QueryRowContext(ctx, `SELECT consumed,cycles FROM qq_bot_periodic_growth_progress WHERE user_id=$1 AND task_id=$2 AND kind=$3`, uid, task.ID, task.Kind).Scan(&consumed, &cycles)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		target := decimal.NewFromFloat(task.Target)
		remaining := decimal.Max(decimal.Zero, progress.value(task.Kind).Sub(consumed))
		reward := task.reward()
		out.Tasks = append(out.Tasks, PreviewTask{ID: "periodic:" + task.ID, Name: task.Name, Progress: periodicProgressText(task.Kind, remaining, target), Eligible: out.Bound && c.Enabled && task.Enabled && reward.enabled() && target.IsPositive() && remaining.GreaterThanOrEqual(target), Status: fmt.Sprintf("已结算 %d 轮", cycles), Reward: reward.description()})
	}
	for _, milestone := range checkin.Milestones {
		progress := growthProgress{total: total, streak: streak}
		task := milestone.task()
		ok, description := progress.task(task)
		reward := milestone.Reward
		status := ""
		if milestone.Repeat {
			consumed, cycles := decimal.Zero, int64(0)
			e := tx.QueryRowContext(ctx, `SELECT consumed,cycles FROM qq_bot_periodic_growth_progress WHERE user_id=$1 AND task_id=$2 AND kind=$3`, uid, milestone.ID, milestone.kind()).Scan(&consumed, &cycles)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return out, e
			}
			target := decimal.NewFromInt(int64(milestone.Days))
			remaining := decimal.Max(decimal.Zero, progress.value(milestone.kind()).Sub(consumed))
			ok = target.IsPositive() && remaining.GreaterThanOrEqual(target)
			description = periodicProgressText(milestone.kind(), remaining, target)
			status = fmt.Sprintf("已结算 %d 轮", cycles)
		} else {
			snapshot, claimed, e := activityRewardSnapshot(ctx, tx, fmt.Sprintf("qq-growth:%d:%s", uid, milestone.ID))
			if e != nil {
				return out, e
			}
			if claimed != "" {
				reward, status, ok = snapshot, claimed, false
			}
		}
		out.CheckinMilestones = append(out.CheckinMilestones, PreviewTask{ID: milestone.ID, Name: milestone.Name, Progress: description, Eligible: out.Bound && c.Enabled && checkin.Enabled && ok && milestone.Enabled && reward.enabled(), Status: status, Reward: reward.description()})
	}
	return out, tx.Commit()
}

func previewPeriodicAmountDays(task GrowthTask, progress growthProgress, cursor, at time.Time) (bool, string) {
	dates := dailyGrowthDates(progress, task)
	unsettled := make([]time.Time, 0, len(dates))
	for _, date := range dates {
		// SQL DATE values use UTC; the cursor denotes a Beijing calendar date.
		if cursor.IsZero() || date.Format("2006-01-02") > cursor.Format("2006-01-02") {
			unsettled = append(unsettled, date)
		}
	}
	count, label := len(unsettled), "未结算累计"
	if isGrowthAmountStreakKind(task.Kind) {
		longest, current := growthAmountDateRuns(unsettled, at)
		count, label = current, "本轮连续"
		// A completed historic run remains payable even after a later gap.
		if float64(longest) >= task.Target {
			count, label = longest, "未结算最长连续"
		}
	}
	source := "消费"
	if growthAmountDaySource(task.Kind) == "recharge" {
		source = "有效充值"
	}
	return task.Target > 0 && float64(count) >= task.Target, fmt.Sprintf("%s %d/%.0f 天（每日%s满 USD %.2f）", label, count, task.Target, source, task.DailyAmount)
}
