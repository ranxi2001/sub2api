package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"github.com/shopspring/decimal"
	"time"
)

var ErrRewardBudget = errors.New("今日余额奖励预算已用尽，请明天再试；本次未发奖、未扣抽奖次数")

// All balance reward writers already hold the app-wide reward lock (seed 272).
// Lock again defensively before reading the ledger, including across replicas.
// The caller's transaction owns both the budget check and all ledger writes.
func checkRewardBudget(ctx context.Context, tx *sql.Tx, c Config, amount float64, at time.Time) error {
	if c.RewardBudgetDaily == nil || amount <= 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,272))`, c.AppID); err != nil {
		return err
	}
	var spent string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount),0)::text FROM qq_bot_rewards WHERE app_id=$1 AND (created_at AT TIME ZONE 'Asia/Shanghai')::date=$2::date`, c.AppID, day(at)).Scan(&spent); err != nil {
		return err
	}
	used, err := decimal.NewFromString(spent)
	if err != nil {
		return err
	}
	if used.Add(decimal.NewFromFloat(amount)).GreaterThan(decimal.NewFromFloat(*c.RewardBudgetDaily)) {
		return ErrRewardBudget
	}
	return nil
}
