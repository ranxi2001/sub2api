package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

const (
	checkinConfigKey  = "qq_bot_checkin_config"
	lotteryConfigKey  = "qq_bot_lottery_config"
	checkinConfigLock = 71427301
	lotteryConfigLock = 71427302
)

type CheckinConfig struct {
	CouponTemplateID   int64                      `json:"coupon_template_id,omitempty"`
	CouponQuantity     int                        `json:"coupon_quantity,omitempty"`
	Milestones         []CheckinMilestone         `json:"milestones"`
	StreakReward       *GrowthReward              `json:"streak_reward,omitempty"`
	StreakDays         int                        `json:"streak_days"`
	StreakChances      int                        `json:"streak_chances"`
	Growth             GrowthConfig               `json:"growth"`
	Enabled            bool                       `json:"enabled"`
	MinAmount          float64                    `json:"min_amount"`
	MaxAmount          float64                    `json:"max_amount"`
	RewardDistribution *CheckinRewardDistribution `json:"reward_distribution,omitempty"`
	RechargeRequirement
}
type InviteConfig struct {
	CouponTemplateID int64   `json:"coupon_template_id,omitempty"`
	CouponQuantity   int     `json:"coupon_quantity,omitempty"`
	Enabled          bool    `json:"enabled"`
	InviteReward     float64 `json:"invite_reward"`
	InviteDailyLimit int     `json:"invite_daily_limit"`
	RechargeRequirement
	enabledOmitted bool
}

func (c *InviteConfig) UnmarshalJSON(data []byte) error {
	type inviteConfigJSON InviteConfig
	decoded := inviteConfigJSON(*c)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var switchField struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(data, &switchField); err != nil {
		return err
	}
	// Old clients cannot change a switch they do not send. Merge under the
	// configuration lock so a concurrent disable cannot be undone.
	decoded.enabledOmitted = switchField.Enabled == nil
	*c = InviteConfig(decoded)
	return nil
}

type LotteryPrize struct {
	CouponTemplateID int64   `json:"coupon_template_id,omitempty"`
	CouponQuantity   int     `json:"coupon_quantity,omitempty"`
	Name             string  `json:"name"`
	Amount           float64 `json:"amount"`
	Chances          int     `json:"chances"`
	ProbabilityBPS   int     `json:"probability_bps"`
}
type LotteryConfig struct {
	Enabled          bool           `json:"enabled"`
	Title            string         `json:"title"`
	RechargeStep     float64        `json:"recharge_step"`
	ChancesPerStep   int64          `json:"chances_per_step"`
	Prizes           []LotteryPrize `json:"prizes"`
	GuaranteeEnabled bool           `json:"guarantee_enabled"`
	GuaranteeEvery   int64          `json:"guarantee_every"`
	GuaranteePrizes  []LotteryPrize `json:"guarantee_prizes"`
}

func defaultLotteryConfig() LotteryConfig {
	return LotteryConfig{
		Title: "幸运抽奖", RechargeStep: 10, ChancesPerStep: 1,
		GuaranteeEvery:  10,
		GuaranteePrizes: []LotteryPrize{{Name: "保底奖励", Amount: 1, ProbabilityBPS: 10000}},
		Prizes: []LotteryPrize{
			{Name: "额度奖励", Amount: 0.1, ProbabilityBPS: 1000},
			{Name: "谢谢参与", Amount: 0, ProbabilityBPS: 9000},
		},
	}
}
func readActivityConfig(ctx context.Context, db queryRower, key string, dst any) (bool, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), dst)
}
func loadCheckinConfig(ctx context.Context, db queryRower, legacy float64) (CheckinConfig, error) {
	c := CheckinConfig{Enabled: legacy > 0, MinAmount: legacy, MaxAmount: legacy,
		RechargeRequirement: RechargeRequirement{RechargeWindowDays: DefaultRechargeWindowDays}}
	_, err := readActivityConfig(ctx, db, checkinConfigKey, &c)
	if c.RewardDistribution == nil {
		distribution := c.rewardDistribution()
		c.RewardDistribution = &distribution
	}
	c.RechargeWindowDays = c.windowDays()
	c.StreakDays = c.streakDays()
	return normalizeCheckinMilestones(c), err
}
func (s *Service) CheckinConfig(ctx context.Context) (CheckinConfig, error) {
	c, err := s.Config(ctx)
	if err != nil {
		return CheckinConfig{}, err
	}
	return loadCheckinConfig(ctx, s.db, c.CheckinReward)
}
func loadLotteryConfig(ctx context.Context, db queryRower) (LotteryConfig, error) {
	c := defaultLotteryConfig()
	_, err := readActivityConfig(ctx, db, lotteryConfigKey, &c)
	return c, err
}
func (s *Service) LotteryConfig(ctx context.Context) (LotteryConfig, error) {
	return loadLotteryConfig(ctx, s.db)
}
func validRewardAmount(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1000 && math.Abs(n*1e8-math.Round(n*1e8)) <= .001
}
func (s *Service) InviteConfig(ctx context.Context) (InviteConfig, error) {
	c, err := s.Config(ctx)
	requirement := c.inviteRechargeRequirement()
	requirement.RechargeWindowDays = requirement.windowDays()
	return InviteConfig{
		Enabled: c.InviteEnabled, InviteReward: c.InviteReward, InviteDailyLimit: c.InviteDailyLimit, CouponTemplateID: c.InviteCouponTemplateID, CouponQuantity: c.InviteCouponQuantity,
		RechargeRequirement: requirement,
	}, err
}
func (s *Service) SaveInviteConfig(ctx context.Context, c InviteConfig) error {
	if err := validateOptionalCoupon(c.CouponTemplateID, c.CouponQuantity); err != nil {
		return err
	}
	if err := validateCouponRewardTemplates(ctx, s.db, c); err != nil {
		return err
	}
	if !validRewardAmount(c.InviteReward) {
		return errors.New("邀请奖励需在 0 到 1000 USD 之间，最多 8 位小数；0 表示不发余额")
	}
	if c.InviteDailyLimit < 0 || c.InviteDailyLimit > 100000 {
		return errors.New("每位邀请人每日奖励次数需在 0 到 100000 之间；0 表示禁止")
	}
	if err := validateRechargeRequirement(c.RechargeRequirement); err != nil {
		return err
	}
	_, err := s.saveConfigChecked(ctx, func(old Config) (Config, error) {
		if err := checkConfigVersion(ctx, inviteConfigOf(old)); err != nil {
			return old, err
		}
		if !c.enabledOmitted {
			old.InviteEnabled = c.Enabled
		}
		old.InviteReward = c.InviteReward
		old.InviteCouponTemplateID = c.CouponTemplateID
		old.InviteCouponQuantity = c.CouponQuantity
		old.InviteDailyLimit = c.InviteDailyLimit
		old.InviteMinRechargeTotal = c.MinRechargeTotal
		old.InviteMinRecharge30d = c.MinRecharge30d
		old.InviteRechargeWindowDays = c.windowDays()
		return old, nil
	}, "")
	return err
}
func validateCheckin(c CheckinConfig) error {
	if err := validateOptionalCoupon(c.CouponTemplateID, c.CouponQuantity); err != nil {
		return err
	}
	if err := validateCheckinMilestones(c); err != nil {
		return err
	}
	if err := validateCheckinDistribution(c.rewardDistribution()); err != nil {
		return err
	}
	if err := validateGrowth(c); err != nil {
		return err
	}
	if !validRewardAmount(c.MinAmount) || !validRewardAmount(c.MaxAmount) || c.MinAmount > c.MaxAmount {
		return errors.New("签到金额需在 0 到 1000 USD 之间，最多 8 位小数，最低金额不能高于最高金额")
	}
	return validateRechargeRequirement(c.RechargeRequirement)
}
func validateLottery(c LotteryConfig) error {
	if strings.TrimSpace(c.Title) == "" || len([]rune(c.Title)) > 60 {
		return errors.New("活动名称需为 1 到 60 个字符")
	}
	if math.IsNaN(c.RechargeStep) || math.IsInf(c.RechargeStep, 0) || c.RechargeStep < .01 || c.RechargeStep > 1e9 || math.Abs(c.RechargeStep*100-math.Round(c.RechargeStep*100)) > .001 {
		return errors.New("累计充值门槛需为 0.01 到 1000000000 USD，最多 2 位小数")
	}
	if c.ChancesPerStep < 1 || c.ChancesPerStep > 1000 {
		return errors.New("每档赠送次数需在 1 到 1000 之间")
	}
	if err := validateLotteryPrizes(c.Prizes); err != nil {
		return err
	}
	if c.GuaranteeEvery < 0 || c.GuaranteeEvery > 1000000 || (c.GuaranteeEnabled && c.GuaranteeEvery == 0) {
		return errors.New("保底周期需为 1 到 1000000 次")
	}
	if c.GuaranteeEnabled {
		if err := validateLotteryPrizes(c.GuaranteePrizes); err != nil {
			return fmt.Errorf("保底奖池：%w", err)
		}
		for _, p := range c.GuaranteePrizes {
			if p.Amount <= 0 && p.Chances <= 0 && p.CouponQuantity <= 0 {
				return errors.New("保底奖项额度或抽奖次数必须大于 0")
			}
		}
	}
	return nil
}
func validateLotteryPrizes(prizes []LotteryPrize) error {
	if len(prizes) < 1 || len(prizes) > 20 {
		return errors.New("请配置 1 到 20 个奖项")
	}
	sum := 0
	for _, p := range prizes {
		if p.CouponQuantity < 0 || p.CouponQuantity > 100 || p.CouponTemplateID < 0 || (p.CouponQuantity > 0 && p.CouponTemplateID == 0) {
			return errors.New("优惠券奖项须选择模板，数量为 0～100")
		}
		if strings.TrimSpace(p.Name) == "" || len([]rune(p.Name)) > 60 || !validRewardAmount(p.Amount) || p.Chances < 0 || p.Chances > 1000 || p.ProbabilityBPS < 1 || p.ProbabilityBPS > 10000 {
			return errors.New("奖项名称、金额、抽奖次数或概率不正确；概率最小 0.01%，金额最多 1000 USD，抽奖次数须为 0～1000")
		}
		sum += p.ProbabilityBPS
	}
	if sum != 10000 {
		return errors.New("所有奖项的中奖概率合计必须为 100%")
	}
	return nil
}
func (s *Service) saveActivityConfig(ctx context.Context, key string, lock int64, c any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lock); err != nil {
		return err
	}
	switch key {
	case checkinConfigKey:
		bot, e := s.loadConfig(ctx, tx)
		if e != nil {
			return e
		}
		current, e := loadCheckinConfig(ctx, tx, bot.CheckinReward)
		if e != nil {
			return e
		}
		section, _ := ctx.Value(checkinSectionContextKey{}).(string)
		if e = checkCheckinVersion(ctx, current, section); e != nil {
			return e
		}
		next, ok := c.(CheckinConfig)
		if !ok {
			return errors.New("invalid checkin configuration")
		}
		previous := current
		// Older clients cannot reset probabilities they do not send. Merge only
		// after taking the configuration lock and checking the draft revision.
		if next.RewardDistribution == nil {
			next.RewardDistribution = current.RewardDistribution
		}
		if next.Milestones == nil {
			next.Milestones = current.Milestones
		}
		switch section {
		case "checkin":
			next.Growth = current.Growth
		case "tasks":
			current.Growth.Tasks = next.Growth.Tasks
			next = current
		case "periodic_tasks":
			current.Growth.PeriodicTasks = next.Growth.PeriodicTasks
			next = current
		case "":
		default:
			return errors.New("未知活动配置模块")
		}
		next = normalizeCheckinMilestones(next)
		if e = validateActivityRewardCadences(previous, next); e != nil {
			return e
		}
		if e = validateCheckin(next); e != nil {
			return e
		}
		c = next
	case lotteryConfigKey:
		current, e := loadLotteryConfig(ctx, tx)
		if e != nil {
			return e
		}
		if e = checkConfigVersion(ctx, current); e != nil {
			return e
		}
	}
	if err := validateCouponRewardTemplates(ctx, tx, c); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=NOW()`, key, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) SaveCheckinConfig(ctx context.Context, c CheckinConfig) error {
	if err := validateCheckin(c); err != nil {
		return err
	}
	c.RechargeWindowDays = c.windowDays()
	c.StreakDays = c.streakDays()
	return s.saveActivityConfig(ctx, checkinConfigKey, checkinConfigLock, c)
}
func (s *Service) SaveLotteryConfig(ctx context.Context, c LotteryConfig) error {
	if err := validateLottery(c); err != nil {
		return err
	}
	return s.saveActivityConfig(ctx, lotteryConfigKey, lotteryConfigLock, c)
}

func lotteryPrizeAt(prizes []LotteryPrize, roll int64) LotteryPrize {
	for _, p := range prizes {
		roll -= int64(p.ProbabilityBPS)
		if roll < 0 {
			return p
		}
	}
	panic("lottery roll outside validated distribution")
}

// Lifetime usage includes ordinary wins and survives rebindings and config changes.
func (c LotteryConfig) drawsUntilGuarantee(used int64) int64 {
	if !c.GuaranteeEnabled || c.GuaranteeEvery <= 0 {
		return 0
	}
	return c.GuaranteeEvery - used%c.GuaranteeEvery
}

// TransferConfig holds the transfer feature's operator-configurable parameters.
type TransferConfig struct {
	TransferEnabled    bool    `json:"transfer_enabled"`
	TransferFeePercent float64 `json:"transfer_fee_percent"`
	TransferMinAmount  float64 `json:"transfer_min_amount"`
	TransferMaxAmount  float64 `json:"transfer_max_amount"`
}

func (s *Service) TransferConfig(ctx context.Context) (TransferConfig, error) {
	c, err := s.Config(ctx)
	if err != nil {
		return TransferConfig{}, err
	}
	return TransferConfig{
		TransferEnabled:    c.TransferEnabled,
		TransferFeePercent: c.TransferFeePercent,
		TransferMinAmount:  c.TransferMinAmount,
		TransferMaxAmount:  c.TransferMaxAmount,
	}, nil
}

func (s *Service) SaveTransferConfig(ctx context.Context, cfg TransferConfig) error {
	_, err := s.saveConfigChecked(ctx, func(old Config) (Config, error) {
		if err := checkConfigVersion(ctx, transferConfigOf(old)); err != nil {
			return old, err
		}
		old.TransferEnabled = cfg.TransferEnabled
		old.TransferFeePercent = cfg.TransferFeePercent
		old.TransferMinAmount = cfg.TransferMinAmount
		old.TransferMaxAmount = cfg.TransferMaxAmount
		return old, nil
	}, "")
	return err
}
