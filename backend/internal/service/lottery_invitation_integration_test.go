//go:build integration

package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/lotteryaccount"
	"github.com/Wei-Shaw/sub2api/ent/lotteryinvitation"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func (f lotteryFixture) invitePool(t *testing.T, mode, recipient string, tickets int64) *service.LotteryPoolView {
	t.Helper()
	in := service.LotteryPoolInput{Name: "Invite " + mode, Mode: mode, Threshold: 0.3, InviteConfig: &service.LotteryInviteConfig{RewardTo: recipient, Tickets: tickets, DailyLimit: 10, IPDailyLimit: 1, DenyUnknownCountry: true}}
	p, err := f.s.SavePool(context.Background(), 0, in)
	require.NoError(t, err)
	_, err = f.s.SavePrize(context.Background(), p.ID, 0, service.LotteryPrizeInput{Name: "Invitation prize", Kind: "text", Text: "Confirmed", Weight: 1, Stock: -1, Enabled: true})
	require.NoError(t, err)
	in.GrantEnabled, in.DrawEnabled = true, true
	p, err = f.s.SavePool(context.Background(), p.ID, in)
	require.NoError(t, err)
	return p
}

func (f lotteryFixture) inviteCode(t *testing.T, pid int64) string {
	t.Helper()
	summary, err := f.s.InviteLink(context.Background(), f.uid, pid)
	require.NoError(t, err)
	return strings.TrimPrefix(summary.Path, "/register?lottery_invite=")
}

func (f lotteryFixture) newInvitee(t *testing.T) lotteryFixture {
	t.Helper()
	u, err := f.c.User.Create().SetEmail(fmt.Sprintf("invite-%d@test.invalid", time.Now().UnixNano())).SetPasswordHash("test-only").SetStatus(service.StatusActive).Save(context.Background())
	require.NoError(t, err)
	f.uid = u.ID
	return f
}

func inviteReceipt(n int) string { return fmt.Sprintf("%064x", n) }

func TestLotteryInvitationPostgres(t *testing.T) {
	dsn := os.Getenv("LOTTERY_TEST_DSN")
	if dsn == "" {
		t.Skip("set LOTTERY_TEST_DSN to an isolated test database")
	}
	require.Contains(t, dsn, "sub2api_lottery_test")
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(20)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	c := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))

	t.Run("visit rewards both parties once and locks only earning rules", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.invitePool(t, "invite_visit", "both", 2)
		code, token := f.inviteCode(t, p.ID), inviteReceipt(1)
		for range 2 {
			v, e := f.s.RecordInviteVisit(ctx, code, token, "203.0.113.1", "US")
			require.NoError(t, e)
			require.Empty(t, v.InviteConfig.BlockedIPs)
		}
		require.EqualValues(t, 2, f.view(t, p.ID).Available)
		visitor := f.newInvitee(t)
		for range 2 {
			require.NoError(t, f.s.RegisterInvitedUser(service.ContextWithLotteryVisitor(ctx, token), visitor.uid))
		}
		require.EqualValues(t, 2, visitor.view(t, p.ID).Available)
		require.EqualValues(t, 2, f.view(t, p.ID).Available)
		in := service.LotteryPoolInput{Name: p.Name, Mode: p.Mode, Threshold: p.Threshold, DrawEnabled: true, GrantEnabled: true, InviteConfig: p.InviteConfig}
		in.InviteConfig.BlockedIPs = []string{"203.0.113.0/24"}
		in.InviteConfig.BlockedCountries = []string{"CN"}
		_, e := f.s.SavePool(ctx, p.ID, in)
		require.NoError(t, e)
		public := f.view(t, p.ID)
		require.Empty(t, public.InviteConfig.BlockedIPs)
		require.Empty(t, public.InviteConfig.BlockedCountries)
		in.InviteConfig.Tickets++
		_, e = f.s.SavePool(ctx, p.ID, in)
		require.Error(t, e)
	})

	t.Run("signup requires new non-self account and does not depend on affiliate rebates", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.invitePool(t, "invite_signup", "inviter", 1)
		code, token := f.inviteCode(t, p.ID), inviteReceipt(2)
		existing := f.newInvitee(t)
		_, e := f.s.RecordInviteVisit(ctx, code, token, "203.0.113.2", "US")
		require.NoError(t, e)
		require.NoError(t, f.s.RegisterInvitedUser(service.ContextWithLotteryVisitor(ctx, token), f.uid))
		require.NoError(t, f.s.RegisterInvitedUser(service.ContextWithLotteryVisitor(ctx, token), existing.uid))
		require.Zero(t, f.view(t, p.ID).Available)
		visitor := f.newInvitee(t)
		require.NoError(t, f.s.RegisterInvitedUser(service.ContextWithLotteryVisitor(ctx, token), visitor.uid))
		require.EqualValues(t, 1, f.view(t, p.ID).Available)
		require.Zero(t, visitor.view(t, p.ID).Available)
		other := f.newInvitee(t)
		require.NoError(t, f.s.RegisterInvitedUser(service.ContextWithLotteryVisitor(ctx, token), other.uid))
		i, e := c.LotteryInvitation.Query().Where(lotteryinvitation.PoolIDEQ(p.ID)).Only(ctx)
		require.NoError(t, e)
		require.Equal(t, visitor.uid, *i.InviteeID)
	})

	t.Run("recharge threshold refund debt duplicate sources and rollback", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.invitePool(t, "invite_recharge", "both", 1)
		code, token := f.inviteCode(t, p.ID), inviteReceipt(3)
		_, e := f.s.RecordInviteVisit(ctx, code, token, "203.0.113.3", "US")
		require.NoError(t, e)
		visitor := f.newInvitee(t)
		require.NoError(t, f.s.RegisterInvitedUser(service.ContextWithLotteryVisitor(ctx, token), visitor.uid))
		f.recharge(t, "admin", fmt.Sprintf("unrelated-%d", f.uid), 1)
		require.Zero(t, f.view(t, p.ID).Available)
		oid := visitor.uid + 100000000
		visitor.recharge(t, "payment", strconv.FormatInt(oid, 10), 0.3)
		visitor.recharge(t, "payment", strconv.FormatInt(oid, 10), 0.3)
		require.EqualValues(t, 1, f.view(t, p.ID).Available)
		require.EqualValues(t, 1, visitor.view(t, p.ID).Available)
		_, e = f.s.Draw(ctx, f.uid, p.ID, fmt.Sprint("invite-", oid))
		require.NoError(t, e)
		visitor.refund(t, oid, 0.1)
		visitor.refund(t, oid, 0.1)
		require.EqualValues(t, 1, f.view(t, p.ID).Debt)
		require.Zero(t, visitor.view(t, p.ID).Available)
		tx, e := c.Tx(ctx)
		require.NoError(t, e)
		require.NoError(t, f.s.RecordRechargeTx(dbent.NewTxContext(ctx, tx), visitor.uid, "admin", fmt.Sprint("rolled-back-", oid), 1))
		require.NoError(t, tx.Rollback())
		require.EqualValues(t, 1, f.view(t, p.ID).Debt)
		visitor.recharge(t, "redeem", fmt.Sprint("recovered-", oid), 0.1)
		require.Zero(t, f.view(t, p.ID).Debt)
		require.Zero(t, f.view(t, p.ID).Available)
		require.EqualValues(t, 1, visitor.view(t, p.ID).Available)
		visitor.recharge(t, "admin", fmt.Sprint("extra-", oid), 0.3)
		require.EqualValues(t, 1, visitor.view(t, p.ID).Available)
		summary, e := f.s.InviteLink(ctx, visitor.uid, p.ID)
		require.NoError(t, e)
		require.Equal(t, 0.6, summary.IncomingProgress)
	})

	t.Run("concurrent same IP visits share a cap across inviters and mapped IPv4", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.invitePool(t, "invite_visit", "inviter", 1)
		other := f.newInvitee(t)
		codes := []string{f.inviteCode(t, p.ID), other.inviteCode(t, p.ID)}
		var accepted atomic.Int32
		var wg sync.WaitGroup
		for n := range 12 {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				address := "203.0.113.4"
				if n%2 == 0 {
					address = "::ffff:203.0.113.4"
				}
				_, e := f.s.RecordInviteVisit(ctx, codes[n%2], inviteReceipt(n+100), address, "US")
				if e == nil {
					accepted.Add(1)
				} else {
					require.ErrorContains(t, e, "limit")
				}
			}(n)
		}
		wg.Wait()
		require.EqualValues(t, 1, accepted.Load())
		require.EqualValues(t, 1, f.view(t, p.ID).Available+other.view(t, p.ID).Available)
	})

	t.Run("country and IP restrictions fail closed without creating participation", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.invitePool(t, "invite_visit", "inviter", 1)
		p.InviteConfig.BlockedCountries = []string{" cn ", "CN"}
		p.InviteConfig.BlockedIPs = []string{"203.0.113.10", "198.51.100.1/24", "2001:db8::/32"}
		_, e := f.s.SavePool(ctx, p.ID, service.LotteryPoolInput{Name: p.Name, Mode: p.Mode, Threshold: p.Threshold, GrantEnabled: true, DrawEnabled: true, InviteConfig: p.InviteConfig})
		require.NoError(t, e)
		code := f.inviteCode(t, p.ID)
		for n, visit := range [][2]string{{"203.0.113.10", "US"}, {"198.51.100.8", "US"}, {"2001:db8::1", "US"}, {"203.0.113.11", "CN"}, {"203.0.113.12", ""}} {
			_, e := f.s.RecordInviteVisit(ctx, code, inviteReceipt(n+200), visit[0], visit[1])
			require.ErrorContains(t, e, "not eligible")
		}
		count, e := c.LotteryInvitation.Query().Where(lotteryinvitation.PoolIDEQ(p.ID)).Count(ctx)
		require.NoError(t, e)
		require.Zero(t, count)
		_, e = f.s.RecordInviteVisit(ctx, code, inviteReceipt(300), "203.0.113.13", "US")
		require.NoError(t, e)
	})

	t.Run("refund racing draw cannot spend stale qualification", func(t *testing.T) {
		f := newLotteryFixture(t, c, db)
		p := f.invitePool(t, "invite_recharge", "inviter", 1)
		code, token := f.inviteCode(t, p.ID), inviteReceipt(400)
		_, e := f.s.RecordInviteVisit(ctx, code, token, "203.0.113.14", "US")
		require.NoError(t, e)
		visitor := f.newInvitee(t)
		require.NoError(t, f.s.RegisterInvitedUser(service.ContextWithLotteryVisitor(ctx, token), visitor.uid))
		oid := visitor.uid + 200000000
		visitor.recharge(t, "payment", strconv.FormatInt(oid, 10), 0.3)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); visitor.refund(t, oid, 0.3) }()
		go func() {
			defer wg.Done()
			_, err := f.s.Draw(ctx, f.uid, p.ID, fmt.Sprint("race-", oid))
			if err != nil {
				require.ErrorContains(t, err, "no available")
			}
		}()
		wg.Wait()
		view := f.view(t, p.ID)
		require.Zero(t, view.Available)
		a, e := c.LotteryAccount.Query().Where(lotteryaccount.PoolIDEQ(p.ID), lotteryaccount.UserIDEQ(f.uid)).Only(ctx)
		require.NoError(t, e)
		require.Zero(t, a.Earned)
		require.Equal(t, a.Used, view.Debt)
	})
}
