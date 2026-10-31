//go:build integration

package service_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/lotteryaccount"
	"github.com/Wei-Shaw/sub2api/ent/lotterycontribution"
	"github.com/Wei-Shaw/sub2api/ent/lotterydraw"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type lotteryFixture struct {
	c     *dbent.Client
	db    *sql.DB
	uid   int64
	s     *service.LotteryService
	users service.UserRepository
	subs  *service.SubscriptionService
}

func newLotteryFixture(t *testing.T, c *dbent.Client, db *sql.DB) lotteryFixture {
	t.Helper()
	u, err := c.User.Create().SetEmail(fmt.Sprintf("lottery-%d@test.invalid", time.Now().UnixNano())).SetPasswordHash("test-only").SetStatus(service.StatusActive).Save(context.Background())
	require.NoError(t, err)
	users := repository.NewUserRepository(c, db)
	subs := service.NewSubscriptionService(repository.NewGroupRepository(c, db), repository.NewUserSubscriptionRepository(c), nil, c, nil)
	t.Cleanup(subs.Stop)
	svc := service.NewLotteryService(c, users, subs, nil, nil, nil)
	_, err = svc.SetEnabled(context.Background(), true)
	require.NoError(t, err)
	return lotteryFixture{c: c, db: db, uid: u.ID, users: users, subs: subs, s: svc}
}

func (f lotteryFixture) pool(t *testing.T, mode string, threshold float64, kind string, stock int64) *service.LotteryPoolView {
	t.Helper()
	ctx := context.Background()
	p, err := f.s.SavePool(ctx, 0, service.LotteryPoolInput{Name: "Test " + mode, Mode: mode, Threshold: threshold})
	require.NoError(t, err)
	_, err = f.s.SavePrize(ctx, p.ID, 0, service.LotteryPrizeInput{Name: "Prize", Kind: kind, Amount: 5, Text: "Thank you", Weight: 1, Stock: stock, Enabled: true})
	require.NoError(t, err)
	p, err = f.s.SavePool(ctx, p.ID, service.LotteryPoolInput{Name: p.Name, Mode: mode, Threshold: threshold, GrantEnabled: true, DrawEnabled: true})
	require.NoError(t, err)
	return p
}

func (f lotteryFixture) recharge(t *testing.T, source, id string, amount float64) {
	t.Helper()
	tx, err := f.c.Tx(context.Background())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	require.NoError(t, f.s.RecordRechargeTx(dbent.NewTxContext(context.Background(), tx), f.uid, source, id, amount))
	require.NoError(t, tx.Commit())
}

func (f lotteryFixture) refund(t *testing.T, oid int64, amount float64) {
	t.Helper()
	tx, err := f.c.Tx(context.Background())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	require.NoError(t, f.s.RefundPaymentTx(dbent.NewTxContext(context.Background(), tx), f.uid, oid, amount))
	require.NoError(t, tx.Commit())
}

func (f lotteryFixture) view(t *testing.T, id int64) service.LotteryPoolView {
	t.Helper()
	ps, err := f.s.Pools(context.Background(), f.uid, false)
	require.NoError(t, err)
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	t.Fatal("pool not found")
	return service.LotteryPoolView{}
}

type lotteryFailingUsers struct{ service.UserRepository }

func (lotteryFailingUsers) AdjustBalance(context.Context, int64, float64) (service.BalanceChange, error) {
	return service.BalanceChange{}, errors.New("injected award failure")
}

func TestLotteryPostgres(t *testing.T) {
	dsn := os.Getenv("LOTTERY_TEST_DSN")
	if dsn == "" {
		t.Skip("set LOTTERY_TEST_DSN to the isolated sub2api_lottery_test database")
	}
	require.Contains(t, dsn, "sub2api_lottery_test", "use a dedicated disposable database")
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(20)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	require.NoError(t, db.PingContext(ctx))
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	c := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))

	t.Run("stacked pools and exact decimal accounting", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		single := f.pool(t, "single", 0.3, "text", -1)
		cumulative := f.pool(t, "cumulative", 0.3, "text", -1)
		f.recharge(t, "redeem", fmt.Sprint(f.uid)+"a", 0.1)
		f.recharge(t, "admin", fmt.Sprint(f.uid)+"b", 0.2)
		require.EqualValues(t, 0, f.view(t, single.ID).Available)
		require.EqualValues(t, 1, f.view(t, cumulative.ID).Available)
		require.Zero(t, f.view(t, cumulative.ID).Progress)
		f.recharge(t, "payment", fmt.Sprint(f.uid)+"c", 0.75)
		f.recharge(t, "payment", fmt.Sprint(f.uid)+"c", 0.75)
		require.EqualValues(t, 1, f.view(t, single.ID).Available)
		require.EqualValues(t, 3, f.view(t, cumulative.ID).Available)
		require.Equal(t, 0.15, f.view(t, cumulative.ID).Progress)
		_, err := f.s.SavePool(ctx, single.ID, service.LotteryPoolInput{Name: "changed", Mode: "single", Threshold: 1})
		require.Error(t, err)
		_, err = f.s.SavePool(ctx, single.ID, service.LotteryPoolInput{Name: single.Name, Mode: "single", Threshold: 0.3, DrawEnabled: true})
		require.NoError(t, err)
		f.recharge(t, "redeem", fmt.Sprint(f.uid)+"d", 1)
		require.EqualValues(t, 1, f.view(t, single.ID).Available)
		_, err = f.s.Draw(ctx, f.uid, single.ID, "stopped-grants-still-draw")
		require.NoError(t, err)
	})

	t.Run("default off retains awards and processes refunds", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.pool(t, "cumulative", 100, "balance", -1)
		_, e := c.Setting.Delete().Where(setting.KeyEQ(service.SettingKeyLotteryEnabled)).Exec(ctx)
		require.NoError(t, e)
		config, e := f.s.Settings(ctx)
		require.NoError(t, e)
		require.False(t, config.Enabled)
		f.recharge(t, "admin", fmt.Sprintf("off-%d", f.uid), 100)
		contributions, e := c.LotteryContribution.Query().Where(lotterycontribution.UserIDEQ(f.uid)).Count(ctx)
		require.NoError(t, e)
		require.Zero(t, contributions)
		_, e = f.s.Draw(ctx, f.uid, p.ID, "disabled")
		require.ErrorContains(t, e, "disabled")
		_, e = f.s.SetEnabled(ctx, true)
		require.NoError(t, e)
		oid := f.uid + 4000000
		f.recharge(t, "payment", strconv.FormatInt(oid, 10), 200)
		draw, e := f.s.Draw(ctx, f.uid, p.ID, "kept-award")
		require.NoError(t, e)
		_, e = f.s.SetEnabled(ctx, false)
		require.NoError(t, e)
		ps, e := f.s.Pools(ctx, f.uid, false)
		require.NoError(t, e)
		require.Empty(t, ps)
		f.refund(t, oid, 200)
		f.refund(t, oid, 200)
		a, e := c.LotteryAccount.Query().Where(lotteryaccount.UserIDEQ(f.uid), lotteryaccount.PoolIDEQ(p.ID)).Only(ctx)
		require.NoError(t, e)
		require.EqualValues(t, 0, a.Earned)
		require.EqualValues(t, 1, a.Used)
		replayed, e := f.s.Draw(ctx, f.uid, p.ID, "kept-award")
		require.NoError(t, e)
		require.Equal(t, draw.ID, replayed.ID)
		_, e = f.s.Draw(ctx, f.uid, p.ID, "blocked-after-disable")
		require.ErrorContains(t, e, "disabled")
		rows, total, e := f.s.Draws(ctx, f.uid, p.ID, 1, 20)
		require.NoError(t, e)
		require.EqualValues(t, 1, total)
		require.Equal(t, draw.ID, rows[0].ID)
		_, e = f.s.SetEnabled(ctx, true)
		require.NoError(t, e)
		f.recharge(t, "admin", fmt.Sprintf("on-%d", f.uid), 150)
		require.Zero(t, f.view(t, p.ID).Available)
		require.Zero(t, f.view(t, p.ID).Debt)
		require.Equal(t, float64(50), f.view(t, p.ID).Progress)
	})

	t.Run("deletion preserves history replay refunds and rejects stale edits", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.pool(t, "single", 100, "text", -1)
		oid := f.uid + 5000000
		f.recharge(t, "payment", strconv.FormatInt(oid, 10), 100)
		draw, e := f.s.Draw(ctx, f.uid, p.ID, "before-delete")
		require.NoError(t, e)
		prize := p.Prizes[0]
		require.NoError(t, f.s.DeletePrize(ctx, p.ID, prize.ID))
		require.NoError(t, f.s.DeletePrize(ctx, p.ID, prize.ID))
		require.Empty(t, f.view(t, p.ID).Prizes)
		_, e = f.s.SavePrize(ctx, p.ID, prize.ID, service.LotteryPrizeInput{Name: "Stale", Kind: "text", Text: "Stale", Weight: 1, Stock: -1, Enabled: true, UpdatedAt: prize.UpdatedAt})
		require.ErrorContains(t, e, "not found")
		require.NoError(t, f.s.DeletePool(ctx, p.ID))
		require.NoError(t, f.s.DeletePool(ctx, p.ID))
		ps, e := f.s.Pools(ctx, 0, true)
		require.NoError(t, e)
		for _, item := range ps {
			require.NotEqual(t, p.ID, item.ID)
		}
		_, e = f.s.SavePool(ctx, p.ID, service.LotteryPoolInput{Name: p.Name, Mode: p.Mode, Threshold: p.Threshold})
		require.ErrorContains(t, e, "not found")
		_, e = f.s.Draw(ctx, f.uid, p.ID, "after-delete")
		require.ErrorContains(t, e, "not found")
		replay, e := f.s.Draw(ctx, f.uid, p.ID, "before-delete")
		require.NoError(t, e)
		require.Equal(t, draw.Prize, replay.Prize)
		f.refund(t, oid, 100)
		f.refund(t, oid, 100)
		a, e := c.LotteryAccount.Query().Where(lotteryaccount.UserIDEQ(f.uid), lotteryaccount.PoolIDEQ(p.ID)).Only(ctx)
		require.NoError(t, e)
		require.EqualValues(t, -1, a.Earned-a.Used)
		f.recharge(t, "admin", fmt.Sprintf("deleted-%d", f.uid), 100)
		count, e := c.LotteryContribution.Query().Where(lotterycontribution.PoolIDEQ(p.ID), lotterycontribution.UserIDEQ(f.uid)).Count(ctx)
		require.NoError(t, e)
		require.Equal(t, 1, count)
	})

	t.Run("deletion and switch changes serialize with drawing", func(t *testing.T) {
		for _, operation := range []string{"pool", "prize", "switch"} {
			t.Run(operation, func(t *testing.T) {
				f := newLotteryFixture(t, c, db)
				p := f.pool(t, "cumulative", 100, "balance", 1)
				f.recharge(t, "admin", fmt.Sprint(f.uid), 200)
				start := make(chan struct{})
				done := make(chan error, 1)
				go func() {
					<-start
					_, e := f.s.Draw(ctx, f.uid, p.ID, "racing-delete")
					done <- e
				}()
				close(start)
				switch operation {
				case "pool":
					require.NoError(t, f.s.DeletePool(ctx, p.ID))
				case "prize":
					require.NoError(t, f.s.DeletePrize(ctx, p.ID, p.Prizes[0].ID))
				case "switch":
					_, e := f.s.SetEnabled(ctx, false)
					require.NoError(t, e)
				}
				drawErr := <-done
				_, e := f.s.Draw(ctx, f.uid, p.ID, "after-control-commit")
				require.Error(t, e)
				u, e := c.User.Get(ctx, f.uid)
				require.NoError(t, e)
				a, e := c.LotteryAccount.Query().Where(lotteryaccount.UserIDEQ(f.uid), lotteryaccount.PoolIDEQ(p.ID)).Only(ctx)
				require.NoError(t, e)
				if drawErr == nil {
					require.Equal(t, float64(5), u.Balance)
					require.EqualValues(t, 1, a.Used)
				} else {
					require.Zero(t, u.Balance)
					require.Zero(t, a.Used)
				}
			})
		}
	})

	t.Run("duplicate draw is atomic and bonus cannot earn tickets", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.pool(t, "cumulative", 100, "balance", -1)
		f.recharge(t, "admin", fmt.Sprint(f.uid), 100)
		var wg sync.WaitGroup
		results := make(chan *service.LotteryDrawView, 8)
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); d, e := f.s.Draw(ctx, f.uid, p.ID, "same-request"); results <- d; errs <- e }()
		}
		wg.Wait()
		close(results)
		close(errs)
		for e := range errs {
			require.NoError(t, e)
		}
		var first int64
		for d := range results {
			if first == 0 {
				first = d.ID
			}
			require.Equal(t, first, d.ID)
		}
		u, e := c.User.Get(ctx, f.uid)
		require.NoError(t, e)
		require.Equal(t, float64(5), u.Balance)
		require.Zero(t, u.TotalRecharged)
		require.Zero(t, f.view(t, p.ID).Available)
		require.Zero(t, f.view(t, p.ID).Progress)
	})

	t.Run("last stock cannot be oversold and stale editor cannot replenish it", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.pool(t, "single", 1, "text", 1)
		g := newLotteryFixture(t, c, db)
		f.recharge(t, "admin", fmt.Sprint(f.uid), 1)
		g.recharge(t, "admin", fmt.Sprint(g.uid), 1)
		old := p.Prizes[0]
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, uid := range []int64{f.uid, g.uid} {
			wg.Add(1)
			go func(uid int64) { defer wg.Done(); _, e := f.s.Draw(ctx, uid, p.ID, "last-stock"); errs <- e }(uid)
		}
		wg.Wait()
		close(errs)
		successes := 0
		for e := range errs {
			if e == nil {
				successes++
			}
		}
		require.Equal(t, 1, successes)
		prize, e := c.LotteryPrize.Get(ctx, old.ID)
		require.NoError(t, e)
		require.Zero(t, prize.Stock)
		_, e = f.s.SavePrize(ctx, p.ID, old.ID, service.LotteryPrizeInput{Name: old.Name, Kind: "text", Text: "Thank you", Weight: 1, Stock: 1, Enabled: true, UpdatedAt: old.UpdatedAt})
		require.Error(t, e)
		require.EqualValues(t, 1, f.view(t, p.ID).Available+g.view(t, p.ID).Available)
	})

	t.Run("award failure rolls back stock and tickets", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.pool(t, "single", 1, "balance", 1)
		f.recharge(t, "admin", fmt.Sprint(f.uid), 1)
		failing := service.NewLotteryService(c, lotteryFailingUsers{f.users}, f.subs, nil, nil, nil)
		_, e := failing.Draw(ctx, f.uid, p.ID, "failed-award")
		require.Error(t, e)
		require.EqualValues(t, 1, f.view(t, p.ID).Available)
		prize, e := c.LotteryPrize.Get(ctx, p.Prizes[0].ID)
		require.NoError(t, e)
		require.EqualValues(t, 1, prize.Stock)
		count, e := c.LotteryDraw.Query().Where(lotterydraw.UserIDEQ(f.uid)).Count(ctx)
		require.NoError(t, e)
		require.Zero(t, count)
		_, e = f.s.Draw(ctx, f.uid, p.ID, "failed-award")
		require.NoError(t, e)
	})

	t.Run("refund debt is repaid by future credits", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.pool(t, "cumulative", 100, "text", -1)
		oid := f.uid + 1000000
		f.recharge(t, "payment", strconv.FormatInt(oid, 10), 250)
		_, e := f.s.Draw(ctx, f.uid, p.ID, "draw-1")
		require.NoError(t, e)
		_, e = f.s.Draw(ctx, f.uid, p.ID, "draw-2")
		require.NoError(t, e)
		f.refund(t, oid, 200)
		f.refund(t, oid, 200)
		v := f.view(t, p.ID)
		require.EqualValues(t, 2, v.Debt)
		require.Equal(t, float64(50), v.Progress)
		require.Zero(t, v.Available)
		f.recharge(t, "admin", fmt.Sprint(f.uid), 150)
		v = f.view(t, p.ID)
		require.Zero(t, v.Debt)
		require.Zero(t, v.Available)
		f.recharge(t, "redeem", fmt.Sprint(f.uid), 100)
		require.EqualValues(t, 1, f.view(t, p.ID).Available)
		draws, total, e := f.s.Draws(ctx, f.uid, p.ID, 1, 20)
		require.NoError(t, e)
		require.EqualValues(t, 2, total)
		require.Len(t, draws, 2)
	})

	t.Run("subscription snapshot extends existing subscription", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		g, e := c.Group.Create().SetName(fmt.Sprintf("lottery-group-%d", f.uid)).SetStatus(service.StatusActive).SetSubscriptionType(service.SubscriptionTypeSubscription).Save(ctx)
		require.NoError(t, e)
		plan, e := c.SubscriptionPlan.Create().SetGroupID(g.ID).SetName("Three days").SetPrice(5).SetValidityDays(3).SetValidityUnit("day").SetForSale(true).Save(ctx)
		require.NoError(t, e)
		p, e := f.s.SavePool(ctx, 0, service.LotteryPoolInput{Name: "Subscription", Mode: "cumulative", Threshold: 1})
		require.NoError(t, e)
		_, e = f.s.SavePrize(ctx, p.ID, 0, service.LotteryPrizeInput{Name: "Subscription prize", Kind: "subscription", PlanID: plan.ID, Weight: 1, Stock: -1, Enabled: true})
		require.NoError(t, e)
		_, e = f.s.SavePool(ctx, p.ID, service.LotteryPoolInput{Name: p.Name, Mode: p.Mode, Threshold: 1, GrantEnabled: true, DrawEnabled: true})
		require.NoError(t, e)
		_, e = c.SubscriptionPlan.UpdateOneID(plan.ID).SetValidityDays(99).Save(ctx)
		require.NoError(t, e)
		f.recharge(t, "admin", fmt.Sprint(f.uid), 3)
		for _, key := range []string{"sub-1", "sub-2"} {
			_, e = f.s.Draw(ctx, f.uid, p.ID, key)
			require.NoError(t, e)
		}
		sub, e := c.UserSubscription.Query().Where(usersubscription.UserIDEQ(f.uid), usersubscription.GroupIDEQ(g.ID)).Only(ctx)
		require.NoError(t, e)
		require.WithinDuration(t, time.Now().Add(6*24*time.Hour), sub.ExpiresAt, 2*time.Minute)
		_, e = c.UserSubscription.UpdateOneID(sub.ID).SetExpiresAt(time.Now().Add(-time.Hour)).SetStatus("expired").Save(ctx)
		require.NoError(t, e)
		_, e = f.s.Draw(ctx, f.uid, p.ID, "sub-expired")
		require.NoError(t, e)
		sub, e = c.UserSubscription.Get(ctx, sub.ID)
		require.NoError(t, e)
		require.WithinDuration(t, time.Now().Add(3*24*time.Hour), sub.ExpiresAt, 2*time.Minute)
		require.Equal(t, service.StatusActive, sub.Status)
		f.recharge(t, "admin", fmt.Sprint(f.uid)+"-extra", 1)
		_, e = c.Group.UpdateOneID(g.ID).SetStatus("disabled").Save(ctx)
		require.NoError(t, e)
		_, e = f.s.Draw(ctx, f.uid, p.ID, "unavailable-sub")
		require.Error(t, e)
		require.EqualValues(t, 1, f.view(t, p.ID).Available)
	})

	t.Run("full refund and draw serialize without losing debt", func(t *testing.T) {
		for i := 0; i < 8; i++ {
			f := newLotteryFixture(t, c, db)
			p := f.pool(t, "single", 100, "balance", -1)
			oid := f.uid + 2000000
			f.recharge(t, "payment", strconv.FormatInt(oid, 10), 100)
			start := make(chan struct{})
			drawDone, refundDone := make(chan error, 1), make(chan error, 1)
			go func() {
				<-start
				_, e := f.s.Draw(ctx, f.uid, p.ID, "racing-refund")
				drawDone <- e
			}()
			go func() {
				<-start
				tx, e := c.Tx(ctx)
				if e == nil {
					e = f.s.RefundPaymentTx(dbent.NewTxContext(ctx, tx), f.uid, oid, 100)
					if e == nil {
						e = tx.Commit()
					} else {
						_ = tx.Rollback()
					}
				}
				refundDone <- e
			}()
			close(start)
			drawErr := <-drawDone
			require.NoError(t, <-refundDone)
			v := f.view(t, p.ID)
			require.Zero(t, v.Available)
			u, e := c.User.Get(ctx, f.uid)
			require.NoError(t, e)
			if drawErr == nil {
				require.EqualValues(t, 1, v.Debt)
				require.Equal(t, float64(5), u.Balance)
			} else {
				require.Zero(t, v.Debt)
				require.Zero(t, u.Balance)
			}
			f.refund(t, oid, 100)
			require.Equal(t, v.Debt, f.view(t, p.ID).Debt)
		}
	})

	t.Run("single partial refunds and history ownership", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.pool(t, "single", 100, "text", -1)
		oid := f.uid + 3000000
		f.recharge(t, "payment", strconv.FormatInt(oid, 10), 250)
		f.refund(t, oid, 150)
		require.EqualValues(t, 1, f.view(t, p.ID).Available)
		f.refund(t, oid, 150.01)
		require.Zero(t, f.view(t, p.ID).Available)
		f.refund(t, oid, 250)
		require.Zero(t, f.view(t, p.ID).Debt)
		f.recharge(t, "admin", fmt.Sprint(f.uid), 100)
		draw, e := f.s.Draw(ctx, f.uid, p.ID, "private-text")
		require.NoError(t, e)
		require.Equal(t, "Thank you", draw.Prize["text"])
		g := newLotteryFixture(t, c, db)
		rows, count, e := f.s.Draws(ctx, g.uid, p.ID, 1, 20)
		require.NoError(t, e)
		require.Zero(t, count)
		require.Empty(t, rows)
		_, e = f.s.Draw(ctx, g.uid, p.ID, "private-text")
		require.Error(t, e)
		require.Empty(t, f.view(t, p.ID).Prizes[0].Text)
	})

	t.Run("all real credit paths grant once", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.pool(t, "cumulative", 100, "text", -1)
		codes := repository.NewRedeemCodeRepository(c)
		redeem := service.ProvideRedeemService(codes, f.users, f.subs, nil, nil, c, nil, nil, f.s)
		code := &service.RedeemCode{Code: fmt.Sprintf("lottery-redeem-%d", f.uid), Type: service.RedeemTypeBalance, Value: 50, Status: service.StatusUnused}
		require.NoError(t, codes.Create(ctx, code))
		_, e := redeem.Redeem(ctx, f.uid, code.Code)
		require.NoError(t, e)
		admin := service.ProvideAdminService(nil, f.users, nil, nil, nil, nil, codes, nil, nil, nil, nil, nil, nil, c, nil, f.subs, repository.NewUserSubscriptionRepository(c), nil, nil, nil, nil, nil, nil, f.s)
		opctx := service.WithAdminBalanceOperation(ctx, "admin:test", fmt.Sprint(f.uid))
		_, e = admin.UpdateUserBalance(opctx, f.uid, 100, "set", "")
		require.NoError(t, e)
		_, e = admin.UpdateUserBalance(opctx, f.uid, 100, "set", "")
		require.NoError(t, e)
		payments := service.ProvidePaymentService(c, payment.NewRegistry(), nil, redeem, f.subs, nil, f.users, nil, nil, nil, f.s)
		order, e := c.PaymentOrder.Create().SetUserID(f.uid).SetUserEmail("lottery@test.invalid").SetUserName("Test").SetAmount(150).SetPayAmount(100).SetRechargeCode(fmt.Sprintf("lottery-pay-%d", f.uid)).SetOutTradeNo(fmt.Sprintf("lottery-order-%d", f.uid)).SetPaymentType("alipay").SetPaymentTradeNo("").SetStatus(service.OrderStatusPaid).SetOrderType(payment.OrderTypeBalance).SetClientIP("127.0.0.1").SetSrcHost("localhost").SetExpiresAt(time.Now().Add(time.Hour)).Save(ctx)
		require.NoError(t, e)
		require.NoError(t, payments.ExecuteBalanceFulfillment(ctx, order.ID))
		require.NoError(t, payments.ExecuteBalanceFulfillment(ctx, order.ID))
		require.EqualValues(t, 2, f.view(t, p.ID).Available)
		u, e := c.User.Get(ctx, f.uid)
		require.NoError(t, e)
		require.Equal(t, float64(250), u.Balance)
		require.Equal(t, float64(50), f.view(t, p.ID).Progress)
		addCtx := service.WithAdminBalanceOperation(ctx, "admin:test", fmt.Sprint(f.uid)+"-add")
		_, e = admin.UpdateUserBalance(addCtx, f.uid, 50, "add", "")
		require.NoError(t, e)
		_, e = admin.UpdateUserBalance(addCtx, f.uid, 50, "add", "")
		require.NoError(t, e)
		require.EqualValues(t, 3, f.view(t, p.ID).Available)
		_, e = admin.UpdateUserBalance(ctx, f.uid, 10, "subtract", "")
		require.NoError(t, e)
		require.EqualValues(t, 3, f.view(t, p.ID).Available)
		completed, e := c.PaymentOrder.Get(ctx, order.ID)
		require.NoError(t, e)
		refund, e := payments.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: completed, RefundAmount: 100, BalanceToDeduct: 100, DeductionType: payment.DeductionTypeBalance})
		require.NoError(t, e)
		require.True(t, refund.Success)
		require.EqualValues(t, 2, f.view(t, p.ID).Available)
		u, e = c.User.Get(ctx, f.uid)
		require.NoError(t, e)
		require.Equal(t, float64(190), u.Balance)
		_, e = payments.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: completed, RefundAmount: 100, BalanceToDeduct: 100, DeductionType: payment.DeductionTypeBalance})
		require.Error(t, e)
		require.EqualValues(t, 2, f.view(t, p.ID).Available)
	})
}
