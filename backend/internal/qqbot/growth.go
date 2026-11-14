package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Zero disables each reward independently. Entitlements are keyed by website
// account, so rebinding, multiple groups and process restarts cannot reset them.
type GrowthConfig struct {
	Tasks             []GrowthTask `json:"tasks"`
	PeriodicTasks     []GrowthTask `json:"periodic_tasks,omitempty"`
	BindingChances    int          `json:"binding_chances"`
	FirstUsageChances int          `json:"first_usage_chances"`
	Checkin30Chances  int          `json:"checkin_30_chances"`
}

func checkinProgress(ctx context.Context, db queryRower, uid int64, at time.Time) (total, streak int, err error) {
	err = db.QueryRowContext(ctx, `WITH dates AS (
 SELECT DISTINCT (created_at AT TIME ZONE 'Asia/Shanghai')::date AS d FROM qq_bot_rewards WHERE user_id=$1 AND kind='checkin' AND created_at <= $2
), ordered AS (SELECT d,ROW_NUMBER() OVER(ORDER BY d DESC)::int AS n,MAX(d) OVER() AS last_day FROM dates)
SELECT COUNT(*),COUNT(*) FILTER(WHERE last_day >= $3::date-1 AND d=last_day-(n-1)) FROM ordered`, uid, at, day(at)).Scan(&total, &streak)
	return
}

func grantStreak(ctx context.Context, tx *sql.Tx, c Config, j Job, uid int64, at time.Time, reward GrowthReward, interval int) (int, bool, error) {
	_, streak, err := checkinProgress(ctx, tx, uid, at)
	if err != nil || !reward.enabled() || streak == 0 || interval < 1 || streak%interval != 0 {
		return streak, false, err
	}
	granted, err := grantActivityReward(ctx, tx, c, j, uid, fmt.Sprintf("qq-streak:%d:%s", uid, day(at)), "streak", fmt.Sprintf("连续签到 %d 天奖励", interval), reward, at)
	return streak, granted, err
}

// Queries and the background worker share the same transaction and account key.
// No command or task number is required to receive an eligible reward.
func (s *Service) growthTasks(ctx context.Context, c Config, j Job) (string, error) {
	text, _, err := s.processGrowthTasks(ctx, c, j, time.Now(), true)
	return text, err
}

func (s *Service) processGrowthTasks(ctx context.Context, c Config, j Job, at time.Time, render bool) (string, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.lockConfig(ctx, tx)
	if err != nil {
		return "", 0, err
	}
	if !current.Enabled || current.AppID != c.AppID || (!current.allows(j.GroupID) || current.groupPolicy(j.GroupID).DisableActivities) {
		return "当前群未启用机器人。", 0, nil
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,272))`, c.AppID); err != nil {
		return "", 0, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, checkinConfigLock); err != nil {
		return "", 0, err
	}
	uid, err := boundUser(ctx, tx, c.AppID, j.MemberID)
	if errors.Is(err, sql.ErrNoRows) {
		return bindFirst, 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	var active int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND status='active' AND deleted_at IS NULL FOR UPDATE`, uid).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return bindFirst, 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	cfg, err := loadCheckinConfig(ctx, tx, current.CheckinReward)
	if err != nil {
		return "", 0, err
	}
	text, grants, balanceChanged, err := settleGrowthForUser(ctx, tx, current, j, uid, cfg, at)
	if err != nil {
		return "", 0, err
	}
	if render {
		periodic, e := rankingGrowthTasks(ctx, tx, current.AppID, uid)
		if e != nil {
			return "", 0, e
		}
		text += "\n\n" + periodic
	}
	if err = tx.Commit(); err != nil {
		return "", 0, err
	}
	if balanceChanged {
		s.invalidate(ctx, uid)
	}
	return text, grants, nil
}

// Caller locks the active user row and check-in configuration before granting.
func settleGrowthForUser(ctx context.Context, tx *sql.Tx, c Config, j Job, uid int64, cfg CheckinConfig, at time.Time) (string, int, bool, error) {
	_, grants, balanceChanged, err := settleCheckinMilestones(ctx, tx, c, j, uid, cfg, at)
	if err != nil {
		return "", 0, false, err
	}
	tasks := cfg.Growth.tasks()
	previousGrants, previousChanged, err := settlePreviousGrowthPeriods(ctx, tx, c, j, uid, tasks, at)
	if err != nil {
		return "", 0, false, err
	}
	grants += previousGrants
	balanceChanged = balanceChanged || previousChanged
	claimed := make([]bool, len(tasks))
	rewards := make([]GrowthReward, len(tasks))
	statuses := make([]string, len(tasks))
	var progressTasks []GrowthTask
	for i, task := range tasks {
		rewards[i] = task.reward()
		snapshot, status, err := activityRewardSnapshot(ctx, tx, growthTaskRewardKey(uid, task, at))
		if err != nil {
			return "", 0, false, err
		}
		claimed[i] = status != ""
		if claimed[i] {
			rewards[i], statuses[i] = snapshot, status
		}
		if !claimed[i] && task.Enabled && task.reward().enabled() {
			progressTasks = append(progressTasks, task)
		}
	}
	for _, task := range cfg.Growth.PeriodicTasks {
		if task.Enabled && task.reward().enabled() {
			progressTasks = append(progressTasks, task)
		}
	}
	progress, err := loadGrowthProgress(ctx, tx, uid, progressTasks, at, true)
	if err != nil {
		return "", 0, false, err
	}
	text := "🌱 **成长任务**\n每日挑战 00:00 重置，每周挑战周一 00:00 重置（北京时间）；自动补结算上一周期。"
	if len(tasks) == 0 {
		text += "\n暂无任务。"
	}
	for i, task := range tasks {
		eligible, status := progress[growthTaskPeriod(task)].task(task)
		switch {
		case claimed[i]:
			status = "已发放"
			if statuses[i] == "pending" {
				status = "待管理员发放"
			}
		case !task.Enabled || !task.reward().enabled():
			status = "未开启"
		case eligible:
			reward := task.reward()
			granted, e := grantActivityReward(ctx, tx, c, j, uid, growthTaskRewardKey(uid, task, at), "growth", growthTaskPeriodLabel(task)+"："+task.Name, reward, at)
			if e != nil {
				if errors.Is(e, ErrRewardBudget) {
					status = "预算不足，等待后续结算"
					break
				}
				return "", 0, false, e
			}
			status = "已发放"
			if granted {
				grants++
				balanceChanged = balanceChanged || reward.Type == "balance"
				status = "已自动发放"
			}
			if reward.Type == "physical" || reward.Type == "other" {
				status = "待管理员发放"
			}
		}
		text += fmt.Sprintf("\n\n**%d. %s · %s**\n状态：%s\n奖励：%s", i+1, replyPlainLabel(task.Name), growthTaskPeriodLabel(task), status, rewards[i].description())
	}
	text += "\n\n达标自动发放；成长成就每账号限一次，每日和每周挑战每个周期限一次。"
	if len(cfg.Growth.PeriodicTasks) > 0 {
		periodic, n, changed, e := settlePeriodicGrowth(ctx, tx, c, j, uid, cfg.Growth.PeriodicTasks, progress["once"], at)
		if e != nil {
			return "", 0, false, e
		}
		text += "\n\n" + periodic
		grants += n
		balanceChanged = balanceChanged || changed
	}
	return text, grants, balanceChanged, nil
}
