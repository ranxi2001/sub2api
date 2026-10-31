package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/shopspring/decimal"
)

type adminBalanceOperationKey struct{}

// Persist the operation identity with the adjustment so a process restart
// between balance commit and HTTP response cannot credit the same request twice.
func WithAdminBalanceOperation(ctx context.Context, actor, key string) context.Context {
	if key == "" {
		return ctx
	}
	hash := sha256.Sum256([]byte(actor + ":" + key))
	return context.WithValue(ctx, adminBalanceOperationKey{}, "ADM-"+base64.RawURLEncoding.EncodeToString(hash[:21]))
}

func (s *adminServiceImpl) updateUserBalanceLottery(ctx context.Context, uid int64, amount float64, operation, notes string) (*User, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if _, err = lotteryLockUser(txCtx, tx.Client(), uid); err != nil {
		return nil, err
	}
	code, _ := ctx.Value(adminBalanceOperationKey{}).(string)
	if code != "" {
		existing, e := s.redeemCodeRepo.GetByCode(txCtx, code)
		if e == nil {
			if existing.UsedBy == nil || *existing.UsedBy != uid {
				return nil, fmt.Errorf("balance operation key was already used for another user")
			}
			return s.userRepo.GetByID(txCtx, uid)
		}
		if !errors.Is(e, ErrRedeemCodeNotFound) {
			return nil, e
		}
	} else {
		code, err = GenerateRedeemCode()
		if err != nil {
			return nil, err
		}
	}
	var change BalanceChange
	switch operation {
	case "set":
		change, err = s.userRepo.SetBalance(txCtx, uid, amount)
	case "add":
		change, err = s.userRepo.AdjustBalance(txCtx, uid, amount)
	case "subtract":
		change, err = s.userRepo.AdjustBalance(txCtx, uid, -amount)
	default:
		return nil, fmt.Errorf("unsupported balance operation: %q", operation)
	}
	if err != nil {
		return nil, err
	}
	delta := decimal.NewFromFloat(change.New).Sub(decimal.NewFromFloat(change.Old)).Round(8).InexactFloat64()
	now := time.Now()
	record := &RedeemCode{Code: code, Type: AdjustmentTypeAdminBalance, Value: delta, Status: StatusUsed, UsedBy: &uid, UsedAt: &now, Notes: notes}
	if err = s.redeemCodeRepo.Create(txCtx, record); err != nil {
		return nil, err
	}
	if delta > 0 {
		if err = s.lottery.RecordRechargeTx(txCtx, uid, "admin", fmt.Sprint(record.ID), delta); err != nil {
			return nil, err
		}
	}
	u, err := s.userRepo.GetByID(txCtx, uid)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.lottery.invalidateAwardCaches(context.WithoutCancel(ctx), uid, &LotteryPrizeView{Kind: "balance"})
	s.tryAccrueAffiliateRebateForAdminRecharge(ctx, uid, operation, amount)
	return u, nil
}
