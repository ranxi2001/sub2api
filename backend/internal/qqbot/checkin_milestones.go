package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const maxCheckinMilestones = 32

// Milestone identities share the original account-scoped growth entitlement
// keys, so moving an existing sign-in task cannot issue its reward twice.
type CheckinMilestone struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Kind    string       `json:"kind"`
	Days    int          `json:"days"`
	Reward  GrowthReward `json:"reward"`
	Enabled bool         `json:"enabled"`
	Repeat  bool         `json:"repeat,omitempty"`
}

func (m CheckinMilestone) reward() GrowthReward { return m.Reward }

func (m CheckinMilestone) kind() string {
	if m.Kind == "" {
		return "checkin_total"
	}
	return m.Kind
}

func (m CheckinMilestone) task() GrowthTask {
	reward := m.Reward
	return GrowthTask{ID: m.ID, Name: m.Name, Kind: m.kind(), Target: float64(m.Days), Reward: &reward, Enabled: m.Enabled}
}

// Normalize at the configuration boundary, without mutating stored settings on
// reads. Old clients and saved templates retain the same rewards and claim keys.
func normalizeCheckinMilestones(c CheckinConfig) CheckinConfig {
	legacyTasks := c.Growth.Tasks == nil
	milestones := append([]CheckinMilestone{}, c.Milestones...)
	seen := make(map[string]bool, len(milestones))
	for _, milestone := range milestones {
		seen[milestone.ID] = true
	}
	move := func(tasks []GrowthTask, repeat bool) []GrowthTask {
		remaining := make([]GrowthTask, 0, len(tasks))
		for _, task := range tasks {
			if task.Kind != "checkin_total" && task.Kind != "checkin_streak" {
				remaining = append(remaining, task)
				continue
			}
			// Do not manufacture an unconfigured, zero-value legacy milestone.
			if !repeat && legacyTasks && task.ID == "checkin_30" && !task.Enabled && !task.reward().enabled() {
				continue
			}
			if !seen[task.ID] {
				milestones = append(milestones, CheckinMilestone{ID: task.ID, Name: task.Name, Kind: task.Kind, Days: int(task.Target), Reward: task.reward(), Enabled: task.Enabled, Repeat: repeat})
				seen[task.ID] = true
			}
		}
		return remaining
	}
	c.Growth.Tasks = move(c.Growth.tasks(), false)
	if c.Growth.PeriodicTasks != nil {
		c.Growth.PeriodicTasks = move(c.Growth.PeriodicTasks, true)
	}
	c.Milestones = milestones
	return c
}

func validateCheckinMilestones(c CheckinConfig) error {
	if len(c.Milestones) > maxCheckinMilestones {
		return fmt.Errorf("最多配置 %d 个签到奖励档位", maxCheckinMilestones)
	}
	ids := make(map[string]bool)
	for _, task := range append(append([]GrowthTask{}, c.Growth.tasks()...), c.Growth.PeriodicTasks...) {
		ids[task.ID] = true
	}
	for _, milestone := range c.Milestones {
		if !growthTaskIDPattern.MatchString(milestone.ID) || ids[milestone.ID] {
			return errors.New("签到奖励档位标识格式不正确或与其他任务重复")
		}
		ids[milestone.ID] = true
		if strings.TrimSpace(milestone.Name) == "" || len([]rune(milestone.Name)) > 40 || strings.IndexFunc(milestone.Name, unicode.IsControl) >= 0 {
			return errors.New("签到奖励名称须为 1～40 个字符且不能换行")
		}
		if milestone.Days < 1 || milestone.Days > 3650 {
			return errors.New("签到奖励目标须为 1～3650 天")
		}
		if milestone.kind() != "checkin_total" && milestone.kind() != "checkin_streak" {
			return errors.New("签到奖励仅支持累计签到或连续签到")
		}
		if milestone.Repeat && milestone.kind() != "checkin_total" {
			return errors.New("循环档位仅支持累计签到，连续签到循环请使用连签奖励配置")
		}
		if err := milestone.Reward.validate(); err != nil {
			return err
		}
	}
	return nil
}

// Changing the schedule changes the grant namespace. A new identity is required
// so editing an existing reward cannot silently create another entitlement.
func validateActivityRewardCadences(current, next CheckinConfig) error {
	cadences := func(cfg CheckinConfig) map[string]string {
		out := make(map[string]string)
		for _, task := range cfg.Growth.Tasks {
			out[task.ID] = growthTaskPeriod(task)
		}
		for _, task := range cfg.Growth.PeriodicTasks {
			out[task.ID] = "repeat"
		}
		for _, milestone := range cfg.Milestones {
			out[milestone.ID] = "once"
			if milestone.Repeat {
				out[milestone.ID] = "repeat"
			}
		}
		return out
	}
	old := cadences(current)
	for id, cadence := range cadences(next) {
		if previous, exists := old[id]; exists && previous != cadence {
			return errors.New("已保存任务的奖励周期不可修改，请新建任务或签到档位；原领取记录会继续保留")
		}
	}
	return nil
}

// Called while holding the configuration and account locks. All reached tiers
// settle independently in the same transaction, including historical catch-up.
func settleCheckinMilestones(ctx context.Context, tx *sql.Tx, c Config, j Job, uid int64, cfg CheckinConfig, at time.Time) (string, int, bool, error) {
	if !cfg.Enabled || len(cfg.Milestones) == 0 {
		return "", 0, false, nil
	}
	total, streak, err := checkinProgress(ctx, tx, uid, at)
	if err != nil {
		return "", 0, false, err
	}
	var text strings.Builder
	grants, changed := 0, false
	for _, milestone := range cfg.Milestones {
		if !milestone.Enabled || !milestone.Reward.enabled() {
			continue
		}
		if milestone.Repeat {
			detail, n, balanceChanged, e := settlePeriodicGrowth(ctx, tx, c, j, uid, []GrowthTask{milestone.task()}, growthProgress{total: total, streak: streak}, at)
			if e != nil {
				return "", 0, false, e
			}
			if n > 0 {
				fmt.Fprintf(&text, "\n\n累计签到循环奖励：%s", strings.TrimPrefix(detail, "**周期性成长任务**"))
			}
			grants += n
			changed = changed || balanceChanged
			continue
		}
		current := total
		if milestone.kind() == "checkin_streak" {
			current = streak
		}
		if current < milestone.Days {
			continue
		}
		granted, e := grantActivityReward(ctx, tx, c, j, uid, fmt.Sprintf("qq-growth:%d:%s", uid, milestone.ID), "growth", "签到奖励："+milestone.Name, milestone.Reward, at)
		if errors.Is(e, ErrRewardBudget) {
			fmt.Fprintf(&text, "\n%s：预算不足，等待后续结算", replyPlainLabel(milestone.Name))
			continue
		}
		if e != nil {
			return "", 0, false, e
		}
		if granted {
			grants++
			changed = changed || milestone.Reward.Type == "balance"
			fmt.Fprintf(&text, "\n\n🎁 %s：%s\n%s", replyPlainLabel(milestone.Name), milestone.Reward.description(), milestone.Reward.deliveryNote())
		}
	}
	return text.String(), grants, changed, nil
}
