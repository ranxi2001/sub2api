package qqbot

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/shopspring/decimal"
)

const maxGrowthTasks = 12

var growthTaskIDPattern = regexp.MustCompile("^[a-z][a-z0-9_-]{0,63}$")

type GrowthTask struct {
	Reward      *GrowthReward `json:"reward,omitempty"`
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Kind        string        `json:"kind"`
	Period      string        `json:"period,omitempty"`
	Target      float64       `json:"target"`
	DailyAmount float64       `json:"daily_amount,omitempty"`
	Chances     int           `json:"chances"`
	Enabled     bool          `json:"enabled"`
}

func (g GrowthConfig) tasks() []GrowthTask {
	if g.Tasks != nil {
		return g.Tasks
	}
	// Keep the original entitlement keys when upgrading existing settings.
	return []GrowthTask{
		{ID: "binding", Name: "完成账号绑定", Kind: "binding", Target: 1, Chances: g.BindingChances, Enabled: g.BindingChances > 0},
		{ID: "first_usage", Name: "首次成功 API 调用", Kind: "usage_count", Target: 1, Chances: g.FirstUsageChances, Enabled: g.FirstUsageChances > 0},
		{ID: "checkin_30", Name: "累计签到 30 天", Kind: "checkin_total", Target: 30, Chances: g.Checkin30Chances, Enabled: g.Checkin30Chances > 0},
	}
}

func (c CheckinConfig) streakDays() int {
	if c.StreakDays == 0 {
		return 7
	}
	return c.StreakDays
}

func validateGrowth(c CheckinConfig) error {
	if err := c.streakReward().validate(); err != nil {
		return err
	}
	if c.streakDays() < 1 || c.streakDays() > 3650 {
		return errors.New("连签奖励周期须为 1～3650 天")
	}
	for _, n := range []int{c.StreakChances, c.Growth.BindingChances, c.Growth.FirstUsageChances, c.Growth.Checkin30Chances} {
		if n < 0 || n > 1000 {
			return errors.New("连签与成长奖励次数须在 0 到 1000 之间；0 表示关闭")
		}
	}
	tasks := c.Growth.tasks()
	if len(tasks) > maxGrowthTasks {
		return fmt.Errorf("最多配置 %d 项成长任务", maxGrowthTasks)
	}
	if len(c.Growth.PeriodicTasks) > maxGrowthTasks {
		return fmt.Errorf("最多配置 %d 项周期任务", maxGrowthTasks)
	}
	for _, task := range c.Growth.PeriodicTasks {
		if growthTaskPeriod(task) != "once" {
			return errors.New("循环任务按累计进度结算，不能同时设置每日或每周重置")
		}
		switch task.Kind {
		case "usage_count", "usage_tokens", "usage_amount", "recharge_total", "checkin_total", "usage_amount_days", "usage_amount_streak", "recharge_days", "recharge_streak":
		default:
			return errors.New("周期任务仅支持累计消费、Token、调用、充值、签到或按日金额达标任务")
		}
	}
	tasks = append(append([]GrowthTask(nil), tasks...), c.Growth.PeriodicTasks...)
	ids := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		period := growthTaskPeriod(task)
		if period != "once" && period != "daily" && period != "weekly" {
			return errors.New("成长任务周期仅支持一次性、每日或每周")
		}
		if period != "once" && (task.Kind == "binding" || task.Kind == "recharge_total" || task.Kind == "checkin_total" || task.Kind == "checkin_streak") {
			return errors.New("绑定、充值和签到里程碑仅支持一次性奖励")
		}
		if period != "once" && isGrowthAmountDayKind(task.Kind) {
			return errors.New("按日金额达标任务仅支持一次性成就或循环任务，不能按每日或每周重置")
		}
		if !growthTaskIDPattern.MatchString(task.ID) || ids[task.ID] {
			return errors.New("成长任务标识格式不正确或重复")
		}
		ids[task.ID] = true
		if strings.TrimSpace(task.Name) == "" || len([]rune(task.Name)) > 40 || strings.IndexFunc(task.Name, unicode.IsControl) >= 0 {
			return errors.New("成长任务名称须为 1～40 个字符且不能换行")
		}
		if task.Chances < 0 || task.Chances > 1000 {
			return errors.New("成长任务奖励次数须为 0～1000 的整数")
		}
		if err := task.reward().validate(); err != nil {
			return err
		}
		if math.IsNaN(task.Target) || math.IsInf(task.Target, 0) {
			return errors.New("成长任务目标不正确")
		}
		switch task.Kind {
		case "usage_amount_days", "usage_amount_streak", "recharge_days", "recharge_streak":
			if task.Target < 1 || task.Target > 3650 || math.Trunc(task.Target) != task.Target {
				return errors.New("金额达标天数目标须为 1～3650 的整数")
			}
			if task.DailyAmount < .01 || !validRechargeThreshold(task.DailyAmount) {
				return errors.New("每日金额门槛须为 0.01～1000000000 USD，最多两位小数")
			}
		case "binding":
			if task.Target != 1 {
				return errors.New("绑定任务目标必须为 1")
			}
		case "usage_count", "checkin_total", "checkin_streak", "usage_days", "model_count", "invite_count":
			limit := float64(3650)
			if task.Kind == "usage_count" || task.Kind == "invite_count" {
				limit = 1000000
			}
			if task.Kind == "model_count" {
				limit = 10000
			}
			if task.Kind == "usage_days" && period == "daily" {
				limit = 1
			} else if task.Kind == "usage_days" && period == "weekly" {
				limit = 7
			}
			if task.Target < 1 || task.Target > limit || math.Trunc(task.Target) != task.Target {
				return errors.New("次数、天数和模型数目标须为范围内的正整数；每日活跃天数最多 1 天，每周最多 7 天")
			}
		case "usage_tokens":
			if task.Target < 1 || task.Target > 1e15 || math.Trunc(task.Target) != task.Target {
				return errors.New("累计 Token 目标须为 1～1000000000000000 的整数")
			}
		case "recharge_total", "usage_amount":
			if task.Target < .01 || !validRechargeThreshold(task.Target) {
				return errors.New("累计金额目标须为 0.01～1000000000 USD，最多两位小数")
			}
		default:
			return errors.New("成长任务类型不支持")
		}
	}
	return nil
}

type growthProgress struct {
	total, streak, usage  int
	days, models, invites int
	recharge              decimal.Decimal
	tokens, spent         decimal.Decimal
	amountDays            map[string][]growthAmountDay
	at                    time.Time
}

func (p growthProgress) task(task GrowthTask) (bool, string) {
	var current float64
	unit := "天"
	switch task.Kind {
	case "usage_amount_days", "usage_amount_streak", "recharge_days", "recharge_streak":
		dates := dailyGrowthDates(p, task)
		count := len(dates)
		eligible := count >= int(task.Target)
		label := "累计达标"
		history := ""
		if isGrowthAmountStreakKind(task.Kind) {
			longest, current := growthAmountDateRuns(dates, p.at)
			count, eligible, label = current, longest >= int(task.Target), "当前连续"
			history = fmt.Sprintf("，历史最长 %d 天", longest)
		}
		action := "消费"
		if growthAmountDaySource(task.Kind) == "recharge" {
			action = "有效充值"
		}
		return eligible, fmt.Sprintf("%s %d/%.0f 天（每日%s满 USD %.2f%s）", label, count, task.Target, action, task.DailyAmount, history)
	case "binding":
		return true, "已完成"
	case "usage_count":
		current, unit = float64(p.usage), "次"
	case "usage_days":
		current = float64(p.days)
	case "model_count":
		current, unit = float64(p.models), "个模型"
	case "invite_count":
		current, unit = float64(p.invites), "人"
	case "checkin_total":
		current = float64(p.total)
	case "checkin_streak":
		current = float64(p.streak)
	case "usage_tokens":
		return p.tokens.GreaterThanOrEqual(decimal.NewFromFloat(task.Target)), fmt.Sprintf("%s/%.0f Token", p.tokens.StringFixed(0), task.Target)
	case "usage_amount":
		return p.spent.GreaterThanOrEqual(decimal.NewFromFloat(task.Target)), fmt.Sprintf("USD %s/%.2f", p.spent.Truncate(2).StringFixed(2), task.Target)
	case "recharge_total":
		return p.recharge.GreaterThanOrEqual(decimal.NewFromFloat(task.Target)), fmt.Sprintf("USD %s/%.2f", p.recharge.Truncate(2).StringFixed(2), task.Target)
	default:
		return false, "任务类型不支持"
	}
	return current >= task.Target, fmt.Sprintf("%.0f/%.0f %s", current, task.Target, unit)
}
