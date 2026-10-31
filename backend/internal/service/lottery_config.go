package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/lotterypool"
	"github.com/Wei-Shaw/sub2api/ent/lotteryprize"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *LotteryService) SavePool(ctx context.Context, id int64, input LotteryPoolInput) (*LotteryPoolView, error) {
	name, err := lotteryName(input.Name)
	if err != nil {
		return nil, err
	}
	threshold, err := lotteryUnits(input.Threshold)
	if err != nil {
		return nil, err
	}
	if threshold < lotteryScale/100 || (input.Mode != "single" && input.Mode != "cumulative") {
		return nil, infraerrors.BadRequest("LOTTERY_INVALID_RULE", "select single/cumulative and a threshold of at least 0.01 USD")
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	var pool *dbent.LotteryPool
	if id == 0 {
		if input.GrantEnabled || input.DrawEnabled {
			return nil, infraerrors.BadRequest("LOTTERY_CONFIGURE_FIRST", "create the pool disabled and configure prizes before enabling it")
		}
		pool, err = c.LotteryPool.Create().SetName(name).SetMode(input.Mode).SetThresholdUnits(threshold).SetSortOrder(input.SortOrder).Save(ctx)
	} else {
		pool, err = c.LotteryPool.Query().Where(lotterypool.IDEQ(id), lotterypool.DeletedAtIsNil()).ForUpdate().Only(ctx)
		if dbent.IsNotFound(err) {
			return nil, infraerrors.NotFound("LOTTERY_POOL_NOT_FOUND", "pool not found")
		}
		if err != nil {
			return nil, err
		}
		if pool.RuleLocked && (input.Mode != pool.Mode || threshold != pool.ThresholdUnits) {
			return nil, infraerrors.Conflict("LOTTERY_RULE_LOCKED", "this pool has recharge history; create a new pool to change the rule")
		}
		// Exhausted pools can always be paused. Enabling either action requires
		// a fulfillable prize, but later exhaustion never prevents recording credits.
		if (input.GrantEnabled && !pool.GrantEnabled) || (input.DrawEnabled && !pool.DrawEnabled) {
			prizes, e := s.prizeViews(ctx, c, id, true)
			if e != nil {
				return nil, e
			}
			available := false
			for _, p := range prizes {
				available = available || p.Available
			}
			if !available {
				return nil, infraerrors.BadRequest("LOTTERY_EMPTY", "add an available prize before enabling the pool")
			}
		}
		update := c.LotteryPool.UpdateOneID(id).SetName(name).SetMode(input.Mode).SetThresholdUnits(threshold).SetSortOrder(input.SortOrder).SetGrantEnabled(input.GrantEnabled).SetDrawEnabled(input.DrawEnabled)
		if pool.ActivatedAt == nil && (input.GrantEnabled || input.DrawEnabled) {
			update.SetActivatedAt(time.Now())
		}
		pool, err = update.Save(ctx)
	}
	if err != nil {
		return nil, err
	}
	prizes, err := s.prizeViews(ctx, c, pool.ID, true)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	v := lotteryPoolView(pool, nil, prizes)
	return &v, nil
}

func (s *LotteryService) SavePrize(ctx context.Context, poolID, prizeID int64, input LotteryPrizeInput) (*LotteryPrizeView, error) {
	name, err := lotteryName(input.Name)
	if err != nil {
		return nil, err
	}
	if input.Weight < 1 || input.Weight > 1000000 || input.Stock < -1 || input.Stock > 1000000000 {
		return nil, infraerrors.BadRequest("LOTTERY_INVALID_PRIZE", "weight must be 1 to 1,000,000; stock must be -1 (unlimited) or 0 to 1,000,000,000")
	}
	if input.Kind != "balance" && input.Kind != "subscription" && input.Kind != "text" {
		return nil, infraerrors.BadRequest("LOTTERY_INVALID_PRIZE", "invalid prize kind")
	}
	amount := int64(0)
	if input.Kind == "balance" {
		amount, err = lotteryUnits(input.Amount)
		if err != nil {
			return nil, err
		}
		if amount <= 0 {
			return nil, infraerrors.BadRequest("LOTTERY_INVALID_AMOUNT", "balance prize must be positive")
		}
	}
	text := ""
	if input.Kind == "text" {
		text = strings.TrimSpace(input.Text)
		if text == "" || utf8.RuneCountInString(text) > 4000 {
			return nil, infraerrors.BadRequest("LOTTERY_INVALID_TEXT", "text must contain 1 to 4,000 characters")
		}
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	if _, err = c.LotteryPool.Query().Where(lotterypool.IDEQ(poolID), lotterypool.DeletedAtIsNil()).ForUpdate().Only(ctx); err != nil {
		if dbent.IsNotFound(err) {
			return nil, infraerrors.NotFound("LOTTERY_POOL_NOT_FOUND", "pool not found")
		}
		return nil, err
	}
	var old *dbent.LotteryPrize
	if prizeID > 0 {
		old, err = c.LotteryPrize.Query().Where(lotteryprize.IDEQ(prizeID), lotteryprize.PoolIDEQ(poolID), lotteryprize.DeletedAtIsNil()).Only(ctx)
		if err != nil {
			if dbent.IsNotFound(err) {
				return nil, infraerrors.NotFound("LOTTERY_PRIZE_NOT_FOUND", "prize not found")
			}
			return nil, err
		}
		if !old.UpdatedAt.Equal(input.UpdatedAt) {
			return nil, infraerrors.Conflict("LOTTERY_PRIZE_CHANGED", "prize or stock changed; reload before saving")
		}
	} else {
		count, e := c.LotteryPrize.Query().Where(lotteryprize.PoolIDEQ(poolID), lotteryprize.DeletedAtIsNil()).Count(ctx)
		if e != nil {
			return nil, e
		}
		if count >= 32 {
			return nil, infraerrors.BadRequest("LOTTERY_PRIZE_LIMIT", "a pool supports up to 32 prizes")
		}
	}
	var planID, groupID int64
	var days int
	var planName string
	if input.Kind == "subscription" {
		if old != nil && old.Kind == "subscription" && old.PlanID == input.PlanID {
			planID, groupID, days, planName = old.PlanID, old.GroupID, old.ValidityDays, old.PlanName
		} else {
			plan, e := c.SubscriptionPlan.Get(ctx, input.PlanID)
			if e != nil && !dbent.IsNotFound(e) {
				return nil, e
			}
			if dbent.IsNotFound(e) || !plan.ForSale {
				return nil, infraerrors.BadRequest("LOTTERY_INVALID_PLAN", "select an existing plan that is for sale")
			}
			planID, groupID, days, planName = plan.ID, plan.GroupID, psComputeValidityDays(plan.ValidityDays, plan.ValidityUnit), plan.Name
			if days <= 0 || days > MaxValidityDays {
				return nil, infraerrors.BadRequest("LOTTERY_INVALID_PLAN", "plan duration exceeds subscription limits")
			}
		}
		if input.Enabled {
			ok, e := c.Group.Query().Where(group.IDEQ(groupID), group.StatusEQ(StatusActive), group.SubscriptionTypeEQ(SubscriptionTypeSubscription), group.DeletedAtIsNil()).Exist(ctx)
			if e != nil {
				return nil, e
			}
			if !ok {
				return nil, infraerrors.BadRequest("LOTTERY_INVALID_PLAN", "subscription group is not available")
			}
		}
	}
	var prize *dbent.LotteryPrize
	if old == nil {
		prize, err = c.LotteryPrize.Create().SetPoolID(poolID).SetName(name).SetKind(input.Kind).SetWeight(input.Weight).SetStock(input.Stock).SetEnabled(input.Enabled).SetSortOrder(input.SortOrder).SetAmountUnits(amount).SetPlanID(planID).SetPlanName(planName).SetGroupID(groupID).SetValidityDays(days).SetText(text).Save(ctx)
	} else {
		prize, err = c.LotteryPrize.UpdateOneID(old.ID).SetName(name).SetKind(input.Kind).SetWeight(input.Weight).SetStock(input.Stock).SetEnabled(input.Enabled).SetSortOrder(input.SortOrder).SetAmountUnits(amount).SetPlanID(planID).SetPlanName(planName).SetGroupID(groupID).SetValidityDays(days).SetText(text).Save(ctx)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	v := lotteryPrizeView(prize)
	return &v, nil
}
