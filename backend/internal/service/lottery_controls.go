package service

import (
	"context"
	"strconv"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/lotterypool"
	"github.com/Wei-Shaw/sub2api/ent/lotteryprize"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const SettingKeyLotteryEnabled = "lottery_enabled"

type LotterySettings struct {
	Enabled bool `json:"enabled"`
}

// Shared locks serialize the switch with grants/draws while allowing unrelated
// users to transact concurrently. Lock order: user -> setting -> pools.
func lotteryEnabled(ctx context.Context, c *dbent.Client, lock bool) (bool, error) {
	q := c.Setting.Query().Where(setting.KeyEQ(SettingKeyLotteryEnabled))
	if lock {
		q.ForShare()
	}
	row, err := q.Only(ctx)
	if dbent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.Value == "true", nil
}

func (s *LotteryService) Settings(ctx context.Context) (*LotterySettings, error) {
	enabled, err := lotteryEnabled(ctx, s.client, false)
	return &LotterySettings{Enabled: enabled}, err
}

func (s *LotteryService) SetEnabled(ctx context.Context, enabled bool) (*LotterySettings, error) {
	err := s.client.Setting.Create().SetKey(SettingKeyLotteryEnabled).SetValue(strconv.FormatBool(enabled)).OnConflictColumns(setting.FieldKey).UpdateNewValues().Exec(ctx)
	if err != nil {
		return nil, err
	}
	if s.settings != nil && s.settings.onUpdate != nil {
		s.settings.onUpdate()
	}
	return &LotterySettings{Enabled: enabled}, nil
}

// Retain account/contribution rows and immutable award snapshots for refunds
// and history. Deleted configuration cannot be restored by a stale save.
func (s *LotteryService) DeletePool(ctx context.Context, id int64) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	p, err := c.LotteryPool.Query().Where(lotterypool.IDEQ(id)).ForUpdate().Only(ctx)
	if dbent.IsNotFound(err) {
		return infraerrors.NotFound("LOTTERY_POOL_NOT_FOUND", "pool not found")
	}
	if err != nil {
		return err
	}
	if p.DeletedAt == nil {
		if _, err = c.LotteryPool.UpdateOneID(id).SetDeletedAt(time.Now()).SetGrantEnabled(false).SetDrawEnabled(false).Save(ctx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *LotteryService) DeletePrize(ctx context.Context, poolID, prizeID int64) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	if _, err = c.LotteryPool.Query().Where(lotterypool.IDEQ(poolID)).ForUpdate().Only(ctx); err != nil {
		if dbent.IsNotFound(err) {
			return infraerrors.NotFound("LOTTERY_POOL_NOT_FOUND", "pool not found")
		}
		return err
	}
	p, err := c.LotteryPrize.Query().Where(lotteryprize.IDEQ(prizeID), lotteryprize.PoolIDEQ(poolID)).Only(ctx)
	if dbent.IsNotFound(err) {
		return infraerrors.NotFound("LOTTERY_PRIZE_NOT_FOUND", "prize not found")
	}
	if err != nil {
		return err
	}
	if p.DeletedAt == nil {
		if _, err = c.LotteryPrize.UpdateOneID(prizeID).SetDeletedAt(time.Now()).SetEnabled(false).Save(ctx); err != nil {
			return err
		}
	}
	return tx.Commit()
}
