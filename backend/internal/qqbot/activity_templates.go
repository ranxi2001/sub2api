package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type ActivityBundle struct {
	Version string        `json:"version"`
	Checkin CheckinConfig `json:"checkin"`
	Lottery LotteryConfig `json:"lottery"`
	Invite  InviteConfig  `json:"invite"`
	Ranking RankingConfig `json:"ranking"`
	Budget  *float64      `json:"budget"`
}
type ActivityTemplateRequest struct {
	Version   string   `json:"version"`
	Template  string   `json:"template"`
	Budget    *float64 `json:"budget"`
	Confirmed bool     `json:"confirmed"`
}

func loadActivityBundle(ctx context.Context, db queryRower, c Config) (ActivityBundle, error) {
	b := ActivityBundle{Invite: inviteConfigOf(c), Budget: c.RewardBudgetDaily}
	var err error
	if b.Checkin, err = loadCheckinConfig(ctx, db, c.CheckinReward); err != nil {
		return b, err
	}
	if b.Lottery, err = loadLotteryConfig(ctx, db); err != nil {
		return b, err
	}
	if b.Ranking, err = loadRankingConfig(ctx, db, c.AppID); err != nil {
		return b, err
	}
	b.Version = ConfigVersion([]any{c.AppID, b})
	return b, nil
}
func (s *Service) ActivityTemplates(ctx context.Context) (map[string]ActivityBundle, error) {
	// Obtain a consistent snapshot against all existing independently saved modules.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := s.loadConfig(ctx, tx)
	if err != nil {
		return nil, err
	}
	current, err := loadActivityBundle(ctx, tx, c)
	if err != nil {
		return nil, err
	}
	out := map[string]ActivityBundle{"current": current}
	for _, name := range []string{"light", "active"} {
		out[name] = activityTemplate(current, name)
	}
	return out, nil
}
func activityTemplate(old ActivityBundle, name string) ActivityBundle {
	raw, _ := json.Marshal(old)
	var b ActivityBundle
	_ = json.Unmarshal(raw, &b)
	b.Checkin.Enabled = true
	b.Checkin.MinAmount = .01
	b.Checkin.MaxAmount = .05
	b.Checkin.RechargeRequirement = RechargeRequirement{RechargeWindowDays: DefaultRechargeWindowDays}
	zero := GrowthReward{Type: "balance"}
	b.Checkin.StreakReward = &zero
	b.Checkin.StreakChances = 0
	tasks := b.Checkin.Growth.tasks()
	for _, spec := range []struct {
		id, name, kind string
		amount         float64
	}{{"binding", "完成账号绑定", "binding", .01}, {"first_usage", "首次成功 API 调用", "usage_count", .02}} {
		reward := GrowthReward{Type: "balance", Amount: spec.amount}
		task := GrowthTask{ID: spec.id, Name: spec.name, Kind: spec.kind, Target: 1, Enabled: true, Reward: &reward}
		found := false
		for i := range tasks {
			if tasks[i].ID == spec.id {
				tasks[i] = task
				found = true
			}
		}
		if !found {
			tasks = append(tasks, task)
		}
	}
	b.Checkin.Growth.Tasks = tasks
	b.Lottery.Enabled = name == "active"
	b.Lottery.Title = "幸运抽奖"
	b.Lottery.RechargeStep = 10
	b.Lottery.ChancesPerStep = 1
	b.Lottery.Prizes = []LotteryPrize{{Name: "普通奖励", Amount: .01, ProbabilityBPS: 9000}, {Name: "幸运奖励", Amount: .1, ProbabilityBPS: 1000}}
	b.Lottery.GuaranteeEnabled = false
	b.Invite = InviteConfig{Enabled: name == "active", InviteReward: .05, InviteDailyLimit: 10, RechargeRequirement: RechargeRequirement{RechargeWindowDays: DefaultRechargeWindowDays}}
	b.Ranking.RewardsEnabled = name == "active"
	b.Ranking.TokenRewards = []RankingPrize{}
	b.Ranking.AmountRewards = []RankingPrize{}
	if name == "active" {
		b.Ranking.Enabled = true
		for i, amount := range []float64{.1, .05, .02} {
			b.Ranking.TokenRewards = append(b.Ranking.TokenRewards, RankingPrize{Rank: i + 1, Reward: GrowthReward{Type: "balance", Amount: amount}})
		}
	}
	return b
}
func (s *Service) ApplyActivityTemplate(ctx context.Context, req ActivityTemplateRequest) (Config, error) {
	if req.Template != "light" && req.Template != "active" {
		return Config{}, errors.New("未知活动模板")
	}
	if !req.Confirmed || req.Budget == nil || *req.Budget <= 0 {
		return Config{}, errors.New("请核对模板规则并设置大于 0 的每日奖励硬预算")
	}
	var oldBot Config
	var committed Config
	result, err := s.saveConfigTransaction(ctx, func(_ *sql.Tx, c Config) (Config, error) {
		if c.AppID == "" {
			return c, errors.New("请先保存机器人 AppID")
		}
		oldBot = c
		c.RewardBudgetDaily = req.Budget
		return c, nil
	}, "limits", func(tx *sql.Tx, c Config) error {
		for _, lock := range []int64{checkinConfigLock, lotteryConfigLock} {
			if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lock); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,288))`, c.AppID); err != nil {
			return err
		}
		current, err := loadActivityBundle(ctx, tx, oldBot)
		if err != nil {
			return err
		}
		if req.Version == "" || req.Version != current.Version {
			return ErrConfigConflict
		}
		next := activityTemplate(current, req.Template)
		next.Budget = req.Budget
		if err = validateCheckin(next.Checkin); err != nil {
			return err
		}
		if err = validateLottery(next.Lottery); err != nil {
			return err
		}
		if err = validateRankingConfig(next.Ranking); err != nil {
			return err
		}
		next.Ranking.EffectiveDay = beijingDayStart(time.Now()).AddDate(0, 0, 1).Format("2006-01-02")
		for key, value := range map[string]any{checkinConfigKey: next.Checkin, lotteryConfigKey: next.Lottery, rankingConfigKey + c.AppID: next.Ranking} {
			raw, e := json.Marshal(value)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=NOW()`, key, string(raw)); e != nil {
				return e
			}
		}
		raw, err := json.Marshal(next.Ranking)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_ranking_config_versions(app_id,effective_day,config) VALUES($1,$2,$3::jsonb) ON CONFLICT(app_id,effective_day) DO UPDATE SET config=EXCLUDED.config,updated_at=NOW()`, c.AppID, next.Ranking.EffectiveDay, string(raw)); err != nil {
			return err
		}
		// Patch invitation fields in the encrypted configuration already written by saveConfigTransaction.
		rawInvite, err := json.Marshal(map[string]any{"invite_enabled": next.Invite.Enabled, "invite_reward": next.Invite.InviteReward, "invite_coupon_template_id": next.Invite.CouponTemplateID, "invite_coupon_quantity": next.Invite.CouponQuantity, "invite_daily_limit": next.Invite.InviteDailyLimit, "invite_min_recharge_total": 0, "invite_min_recharge_30d": 0, "invite_recharge_window_days": DefaultRechargeWindowDays})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE settings SET value=(value::jsonb || $2::jsonb)::text WHERE key=$1`, configKey, string(rawInvite))
		if err != nil {
			return err
		}
		history := []ConfigChange{}
		if _, err = readActivityConfig(ctx, tx, configHistoryKey, &history); err != nil {
			return err
		}
		before, _ := json.Marshal(current)
		after, _ := json.Marshal(next)
		history = append([]ConfigChange{{At: time.Now(), Section: "activity-template", Before: map[string]json.RawMessage{"rules": before}, After: map[string]json.RawMessage{"rules": after}}}, history...)
		if len(history) > 30 {
			history = history[:30]
		}
		rawHistory, err := json.Marshal(history)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=NOW()`, configHistoryKey, string(rawHistory)); err != nil {
			return err
		}
		committed, err = s.loadConfig(ctx, tx)
		return err
	})
	if err != nil {
		return result, err
	}
	return committed.Public(), nil
}
