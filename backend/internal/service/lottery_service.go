package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/lotteryaccount"
	"github.com/Wei-Shaw/sub2api/ent/lotterycontribution"
	"github.com/Wei-Shaw/sub2api/ent/lotterydraw"
	"github.com/Wei-Shaw/sub2api/ent/lotteryledger"
	"github.com/Wei-Shaw/sub2api/ent/lotterypool"
	"github.com/Wei-Shaw/sub2api/ent/lotteryprize"
	"github.com/Wei-Shaw/sub2api/ent/user"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type ctxSkipLotteryRecharge struct{}

// Payment credits use an internal redeem code; only order completion grants tickets.
func contextSkipLotteryRecharge(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxSkipLotteryRecharge{}, true)
}

func lotteryTxClient(ctx context.Context) (*dbent.Client, error) {
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return nil, fmt.Errorf("lottery accounting requires a transaction")
	}
	return tx.Client(), nil
}

// All monetary paths lock user -> pools in ID order -> account/prizes. A pool
// configuration change only locks the pool, so it cannot invert this order.
func lotteryLockUser(ctx context.Context, c *dbent.Client, uid int64) (*dbent.User, error) {
	return c.User.Query().Where(user.IDEQ(uid), user.DeletedAtIsNil()).ForUpdate().Only(ctx)
}

func lotteryAccountForUpdate(ctx context.Context, c *dbent.Client, pid, uid int64) (*dbent.LotteryAccount, error) {
	a, err := c.LotteryAccount.Query().Where(lotteryaccount.PoolIDEQ(pid), lotteryaccount.UserIDEQ(uid)).Only(ctx)
	if dbent.IsNotFound(err) {
		return c.LotteryAccount.Create().SetPoolID(pid).SetUserID(uid).Save(ctx)
	}
	return a, err
}

// RecordRechargeTx is committed with the successful originating balance operation.
func (s *LotteryService) RecordRechargeTx(ctx context.Context, uid int64, source, sourceID string, amount float64) error {
	if s == nil || ctx.Value(ctxSkipLotteryRecharge{}) != nil || amount <= 0 {
		return nil
	}
	units, err := lotteryUnits(amount)
	if err != nil {
		return err
	}
	if units == 0 {
		return nil
	}
	c, err := lotteryTxClient(ctx)
	if err != nil {
		return err
	}
	if _, err := lotteryLockUser(ctx, c, uid); err != nil {
		return err
	}
	enabled, err := lotteryEnabled(ctx, c, true)
	if err != nil || !enabled {
		return err
	}
	pools, err := c.LotteryPool.Query().Where(lotterypool.DeletedAtIsNil(), lotterypool.GrantEnabledEQ(true)).Order(dbent.Asc(lotterypool.FieldID)).ForUpdate().All(ctx)
	if err != nil {
		return err
	}
	for _, p := range pools {
		exists, err := c.LotteryContribution.Query().Where(lotterycontribution.PoolIDEQ(p.ID), lotterycontribution.SourceTypeEQ(source), lotterycontribution.SourceIDEQ(sourceID)).Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		a, err := lotteryAccountForUpdate(ctx, c, p.ID, uid)
		if err != nil {
			return err
		}
		delta, err := lotteryTicketDelta(p.Mode, p.ThresholdUnits, a.TotalUnits, 0, units)
		if err != nil {
			return err
		}
		if _, err = c.LotteryContribution.Create().SetPoolID(p.ID).SetUserID(uid).SetSourceType(source).SetSourceID(sourceID).SetAmountUnits(units).SetNetUnits(units).Save(ctx); err != nil {
			return err
		}
		if _, err = c.LotteryAccount.UpdateOneID(a.ID).AddTotalUnits(units).AddEarned(delta).Save(ctx); err != nil {
			return err
		}
		if _, err = c.LotteryPool.UpdateOneID(p.ID).SetRuleLocked(true).Save(ctx); err != nil {
			return err
		}
		if _, err = c.LotteryLedger.Create().SetPoolID(p.ID).SetUserID(uid).SetAction("recharge").SetSourceType(source).SetSourceID(sourceID).SetAmountUnits(units).SetTicketDelta(delta).Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

// RefundPaymentTx accepts the cumulative refunded USD amount, making retries
// idempotent. Awarded prizes are retained; earned-used may become negative.
func (s *LotteryService) RefundPaymentTx(ctx context.Context, uid, orderID int64, refunded float64) error {
	if s == nil {
		return nil
	}
	units, err := lotteryUnits(refunded)
	if err != nil {
		return err
	}
	c, err := lotteryTxClient(ctx)
	if err != nil {
		return err
	}
	if _, err := lotteryLockUser(ctx, c, uid); err != nil {
		return err
	}
	sourceID := strconv.FormatInt(orderID, 10)
	contributions, err := c.LotteryContribution.Query().Where(lotterycontribution.UserIDEQ(uid), lotterycontribution.SourceTypeEQ("payment"), lotterycontribution.SourceIDEQ(sourceID)).Order(dbent.Asc(lotterycontribution.FieldPoolID)).All(ctx)
	if err != nil {
		return err
	}
	for _, contribution := range contributions {
		p, err := c.LotteryPool.Query().Where(lotterypool.IDEQ(contribution.PoolID)).ForUpdate().Only(ctx)
		if err != nil {
			return err
		}
		net := contribution.AmountUnits - units
		if net < 0 {
			net = 0
		}
		if net >= contribution.NetUnits {
			continue
		}
		a, err := lotteryAccountForUpdate(ctx, c, p.ID, uid)
		if err != nil {
			return err
		}
		delta, err := lotteryTicketDelta(p.Mode, p.ThresholdUnits, a.TotalUnits, contribution.NetUnits, net)
		if err != nil {
			return err
		}
		amountDelta := net - contribution.NetUnits
		if _, err = c.LotteryContribution.UpdateOneID(contribution.ID).SetNetUnits(net).Save(ctx); err != nil {
			return err
		}
		if _, err = c.LotteryAccount.UpdateOneID(a.ID).AddTotalUnits(amountDelta).AddEarned(delta).Save(ctx); err != nil {
			return err
		}
		if _, err = c.LotteryLedger.Create().SetPoolID(p.ID).SetUserID(uid).SetAction("refund").SetSourceType("payment").SetSourceID(sourceID).SetAmountUnits(amountDelta).SetTicketDelta(delta).Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *LotteryService) prizeViews(ctx context.Context, c *dbent.Client, poolID int64, admin bool) ([]LotteryPrizeView, error) {
	prizes, err := c.LotteryPrize.Query().Where(lotteryprize.PoolIDEQ(poolID), lotteryprize.DeletedAtIsNil()).Order(dbent.Asc(lotteryprize.FieldSortOrder), dbent.Asc(lotteryprize.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]LotteryPrizeView, 0, len(prizes))
	var total int64
	for _, p := range prizes {
		v := lotteryPrizeView(p)
		if p.Kind == "subscription" && v.Available {
			v.Available, err = c.Group.Query().Where(group.IDEQ(p.GroupID), group.StatusEQ(StatusActive), group.SubscriptionTypeEQ(SubscriptionTypeSubscription), group.DeletedAtIsNil()).Exist(ctx)
			if err != nil {
				return nil, err
			}
		}
		if !admin {
			v.Text = ""
		}
		if v.Available {
			total += v.Weight
		}
		if admin || p.Enabled {
			views = append(views, v)
		}
	}
	for i := range views {
		if views[i].Available && total > 0 {
			views[i].Probability = float64(views[i].Weight) / float64(total)
		}
	}
	return views, nil
}

func (s *LotteryService) Pools(ctx context.Context, uid int64, admin bool) ([]LotteryPoolView, error) {
	if !admin {
		enabled, err := lotteryEnabled(ctx, s.client, false)
		if err != nil {
			return nil, err
		}
		if !enabled {
			return []LotteryPoolView{}, nil
		}
	}
	pools, err := s.client.LotteryPool.Query().Where(lotterypool.DeletedAtIsNil()).Order(dbent.Asc(lotterypool.FieldSortOrder), dbent.Asc(lotterypool.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]LotteryPoolView, 0, len(pools))
	for _, p := range pools {
		var account *dbent.LotteryAccount
		if uid > 0 {
			account, err = s.client.LotteryAccount.Query().Where(lotteryaccount.PoolIDEQ(p.ID), lotteryaccount.UserIDEQ(uid)).Only(ctx)
			if err != nil && !dbent.IsNotFound(err) {
				return nil, err
			}
		}
		if !admin && p.ActivatedAt == nil && account == nil {
			continue
		}
		prizes, err := s.prizeViews(ctx, s.client, p.ID, admin)
		if err != nil {
			return nil, err
		}
		result = append(result, lotteryPoolView(p, account, prizes))
	}
	return result, nil
}

func (s *LotteryService) Draw(ctx context.Context, uid, poolID int64, key string) (*LotteryDrawView, error) {
	originalCtx := ctx
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 {
		return nil, infraerrors.BadRequest("LOTTERY_REQUEST_KEY", "an Idempotency-Key of up to 128 characters is required")
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	ctx = dbent.NewTxContext(ctx, tx)
	u, err := lotteryLockUser(ctx, c, uid)
	if err != nil {
		return nil, err
	}
	if u.Status != StatusActive {
		return nil, infraerrors.Forbidden("LOTTERY_USER_INACTIVE", "user is inactive")
	}
	existing, err := c.LotteryDraw.Query().Where(lotterydraw.UserIDEQ(uid), lotterydraw.RequestKeyEQ(key)).Only(ctx)
	if err == nil {
		if existing.PoolID != poolID {
			return nil, infraerrors.Conflict("LOTTERY_KEY_REUSED", "request key belongs to another pool")
		}
		return lotteryDrawView(existing), nil
	}
	if !dbent.IsNotFound(err) {
		return nil, err
	}
	enabled, err := lotteryEnabled(ctx, c, true)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, infraerrors.Forbidden("LOTTERY_DISABLED", "lottery is disabled")
	}
	p, err := c.LotteryPool.Query().Where(lotterypool.IDEQ(poolID), lotterypool.DeletedAtIsNil()).ForUpdate().Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.NotFound("LOTTERY_POOL_NOT_FOUND", "pool not found")
	}
	if err != nil {
		return nil, err
	}
	if !p.DrawEnabled {
		return nil, infraerrors.Conflict("LOTTERY_PAUSED", "drawing is paused")
	}
	a, err := lotteryAccountForUpdate(ctx, c, poolID, uid)
	if err != nil {
		return nil, err
	}
	if a.Earned <= a.Used {
		return nil, infraerrors.Conflict("LOTTERY_NO_TICKETS", "no available draws")
	}
	prizes, err := s.prizeViews(ctx, c, poolID, true)
	if err != nil {
		return nil, err
	}
	var total int64
	for _, prize := range prizes {
		if prize.Available {
			total += prize.Weight
		}
	}
	if total <= 0 {
		return nil, infraerrors.Conflict("LOTTERY_EMPTY", "no available prizes")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(total))
	if err != nil {
		return nil, err
	}
	selected := selectLotteryPrize(prizes, n.Int64())
	if selected == nil {
		return nil, fmt.Errorf("invalid lottery selection")
	}
	if selected.Stock > 0 {
		if _, err = c.LotteryPrize.UpdateOneID(selected.ID).AddStock(-1).Save(ctx); err != nil {
			return nil, err
		}
	}
	if _, err = c.LotteryAccount.UpdateOneID(a.ID).AddUsed(1).Save(ctx); err != nil {
		return nil, err
	}
	snapshot := map[string]any{}
	raw, err := json.Marshal(selected)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	snapshot["pool_name"] = p.Name
	draw, err := c.LotteryDraw.Create().SetPoolID(poolID).SetUserID(uid).SetPrizeID(selected.ID).SetRequestKey(key).SetPrizeSnapshot(snapshot).Save(ctx)
	if err != nil {
		return nil, err
	}
	switch selected.Kind {
	case "balance":
		if _, err = s.users.AdjustBalance(ctx, uid, selected.Amount); err != nil {
			return nil, err
		}
	case "subscription":
		if _, _, err = s.subscriptions.assignOrExtendSubscription(ctx, &AssignSubscriptionInput{UserID: uid, GroupID: selected.GroupID, ValidityDays: selected.ValidityDays, Notes: fmt.Sprintf("lottery draw:%d", draw.ID)}, true); err != nil {
			return nil, err
		}
	}
	if _, err = c.LotteryLedger.Create().SetPoolID(poolID).SetUserID(uid).SetAction("draw").SetSourceType("draw").SetSourceID(strconv.FormatInt(draw.ID, 10)).SetAmountUnits(0).SetTicketDelta(-1).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateAwardCaches(context.WithoutCancel(originalCtx), uid, selected)
	return lotteryDrawView(draw), nil
}

func selectLotteryPrize(prizes []LotteryPrizeView, n int64) *LotteryPrizeView {
	for i := range prizes {
		if !prizes[i].Available {
			continue
		}
		if n < prizes[i].Weight {
			return &prizes[i]
		}
		n -= prizes[i].Weight
	}
	return nil
}

func (s *LotteryService) invalidateAwardCaches(ctx context.Context, uid int64, prize *LotteryPrizeView) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if s.authCache != nil {
		s.authCache.InvalidateAuthCacheByUserID(ctx, uid)
	}
	if prize.Kind == "subscription" && s.subscriptions != nil {
		s.subscriptions.InvalidateSubCache(uid, prize.GroupID)
	}
	if s.billingCache == nil {
		return
	}
	if prize.Kind == "balance" {
		_ = s.billingCache.InvalidateUserBalance(ctx, uid)
	}
	if prize.Kind == "subscription" {
		_ = s.billingCache.InvalidateSubscription(ctx, uid, prize.GroupID)
	}
}

func (s *LotteryService) Draws(ctx context.Context, uid, pid int64, page, size int) ([]LotteryDrawView, int64, error) {
	q := s.client.LotteryDraw.Query()
	if uid > 0 {
		q.Where(lotterydraw.UserIDEQ(uid))
	}
	if pid > 0 {
		q.Where(lotterydraw.PoolIDEQ(pid))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Order(dbent.Desc(lotterydraw.FieldID)).Offset((page - 1) * size).Limit(size).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	result := make([]LotteryDrawView, 0, len(rows))
	for _, row := range rows {
		result = append(result, *lotteryDrawView(row))
	}
	return result, int64(total), nil
}

func (s *LotteryService) Ledger(ctx context.Context, uid, pid int64, page, size int) ([]LotteryLedgerView, int64, error) {
	q := s.client.LotteryLedger.Query()
	if uid > 0 {
		q.Where(lotteryledger.UserIDEQ(uid))
	}
	if pid > 0 {
		q.Where(lotteryledger.PoolIDEQ(pid))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Order(dbent.Desc(lotteryledger.FieldID)).Offset((page - 1) * size).Limit(size).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	result := make([]LotteryLedgerView, 0, len(rows))
	for _, r := range rows {
		result = append(result, LotteryLedgerView{ID: r.ID, PoolID: r.PoolID, UserID: r.UserID, Action: r.Action, SourceType: r.SourceType, SourceID: r.SourceID, Amount: lotteryAmount(r.AmountUnits), TicketDelta: r.TicketDelta, CreatedAt: r.CreatedAt})
	}
	return result, int64(total), nil
}
