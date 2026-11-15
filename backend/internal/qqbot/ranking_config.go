package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const rankingConfigKey = "qq_bot_ranking_config:"
const rankingTopN = 10

type RankingPrize struct {
	Rank   int          `json:"rank"`
	Reward GrowthReward `json:"reward"`
}

type RankingConfig struct {
	Enabled        bool           `json:"enabled"`
	RewardsEnabled bool           `json:"rewards_enabled"`
	TokenRewards   []RankingPrize `json:"token_rewards"`
	AmountRewards  []RankingPrize `json:"amount_rewards"`
	EffectiveDay   string         `json:"effective_day"`
}

func defaultRankingConfig() RankingConfig {
	prizes := func() []RankingPrize {
		return []RankingPrize{
			{Rank: 1, Reward: GrowthReward{Type: "chances"}},
			{Rank: 2, Reward: GrowthReward{Type: "chances"}},
			{Rank: 3, Reward: GrowthReward{Type: "chances"}},
		}
	}
	return RankingConfig{Enabled: true, TokenRewards: prizes(), AmountRewards: prizes()}
}

func validateRankingConfig(c RankingConfig) error {
	hasReward := false
	for _, prizes := range [][]RankingPrize{c.TokenRewards, c.AmountRewards} {
		if len(prizes) > rankingTopN {
			return errors.New("每个榜单最多配置 10 个名次奖励")
		}
		seen := map[int]bool{}
		for _, prize := range prizes {
			if prize.Rank < 1 || prize.Rank > rankingTopN || seen[prize.Rank] {
				return errors.New("奖励名次须为 1～10 且不能重复")
			}
			seen[prize.Rank] = true
			if err := prize.Reward.validate(); err != nil {
				return err
			}
			hasReward = hasReward || prize.Reward.enabled()
		}
	}
	if c.RewardsEnabled && !hasReward {
		return errors.New("开启每日奖励前，请至少配置一个非零奖励")
	}
	return nil
}

func loadRankingConfig(ctx context.Context, db queryRower, appID string) (RankingConfig, error) {
	c := defaultRankingConfig()
	_, err := readActivityConfig(ctx, db, rankingConfigKey+appID, &c)
	normalizeRankingPrizes(&c)
	return c, err
}

func normalizeRankingPrizes(c *RankingConfig) {
	if c.TokenRewards == nil {
		c.TokenRewards = []RankingPrize{}
	}
	if c.AmountRewards == nil {
		c.AmountRewards = []RankingPrize{}
	}
}

func (s *Service) RankingConfig(ctx context.Context) (RankingConfig, error) {
	bot, err := s.Config(ctx)
	if err != nil {
		return RankingConfig{}, err
	}
	return loadRankingConfig(ctx, s.db, bot.AppID)
}

func (s *Service) SaveRankingConfig(ctx context.Context, c RankingConfig) (RankingConfig, error) {
	return s.saveRankingConfigAt(ctx, c, time.Now())
}

func (s *Service) saveRankingConfigAt(ctx context.Context, c RankingConfig, now time.Time) (RankingConfig, error) {
	if err := validateRankingConfig(c); err != nil {
		return c, err
	}
	if err := validateCouponRewardTemplates(ctx, s.db, c); err != nil {
		return c, err
	}
	normalizeRankingPrizes(&c)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer func() { _ = tx.Rollback() }()
	bot, err := s.lockConfig(ctx, tx)
	if err != nil {
		return c, err
	}
	if bot.AppID == "" {
		return c, errors.New("请先配置 QQ 机器人 AppID")
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,288))`, bot.AppID); err != nil {
		return c, err
	}
	current, err := loadRankingConfig(ctx, tx, bot.AppID)
	if err != nil {
		return c, err
	}
	if err = checkConfigVersion(ctx, current); err != nil {
		return c, err
	}
	c.EffectiveDay = beijingDayStart(now).AddDate(0, 0, 1).Format("2006-01-02")
	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=NOW()`, rankingConfigKey+bot.AppID, string(raw)); err != nil {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_ranking_config_versions(app_id,effective_day,config) VALUES($1,$2,$3::jsonb) ON CONFLICT(app_id,effective_day) DO UPDATE SET config=EXCLUDED.config,updated_at=NOW()`, bot.AppID, c.EffectiveDay, string(raw)); err != nil {
		return c, err
	}
	return c, tx.Commit()
}

func rankingRulesForDay(ctx context.Context, tx *sql.Tx, appID, date string) (RankingConfig, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT config FROM qq_bot_ranking_config_versions WHERE app_id=$1 AND effective_day <= $2::date ORDER BY effective_day DESC LIMIT 1`, appID, date).Scan(&raw)
	if err != nil {
		return RankingConfig{}, err
	}
	var c RankingConfig
	if err = json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	if err = validateRankingConfig(c); err != nil {
		return c, fmt.Errorf("invalid ranking reward rules: %w", err)
	}
	return c, nil
}
