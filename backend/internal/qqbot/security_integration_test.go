//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func waitForDatabaseBlocker(t *testing.T, db *sql.DB, pid int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var blocked bool
		err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked)
		return err == nil && blocked
	}, 3*time.Second, 10*time.Millisecond, "operation should wait for the held database lock")
}

func TestLotteryExcludesPaymentCodesEvenIfRedeemedByAnotherAccount(t *testing.T) {
	s, db, c, uid, _ := lotteryFixture(t, 0)
	owner := user(t, db)
	_, err := db.Exec(`INSERT INTO payment_orders(user_id,amount,status,recharge_code)
 VALUES($1,100,'REFUNDED','payment-owned-code')`, owner)
	require.NoError(t, err)
	// Inconsistent legacy or imported records still belong to the online order;
	// they must not turn into standalone recharge credit for another account.
	_, err = db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at)
 VALUES('payment-owned-code','balance',100,'used',$1,NOW())`, uid)
	require.NoError(t, err)
	state, err := s.LotteryState(context.Background(), uid)
	require.NoError(t, err)
	require.Zero(t, state.RechargeTotal)
	reply, err := s.drawLottery(context.Background(), c, Job{ID: "mismatched-order-code", MemberID: "member", GroupID: "group"})
	require.NoError(t, err)
	require.Contains(t, reply, "抽奖次数不足")
}

func TestInvitationSettlementWaitsForConcurrentDeparture(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	inviter, invitee := user(t, db), user(t, db)
	bind(t, db, inviter, "inviter")
	bind(t, db, invitee, "invitee")
	_, err = db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at)
 VALUES('app','group','invitee','inviter',NOW()-INTERVAL '1 minute',NOW())`)
	require.NoError(t, err)
	departure, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = departure.Rollback() }()
	var pid int
	require.NoError(t, departure.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	_, err = departure.ExecContext(ctx, `UPDATE qq_bot_members SET left_at=NOW() WHERE member_id='invitee'`)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := s.reward(ctx, c, Job{AppID: c.AppID, GroupID: "group", MemberID: "invitee"}, "invite")
		done <- err
	}()
	waitForDatabaseBlocker(t, db, pid)
	require.NoError(t, departure.Commit())
	require.NoError(t, <-done)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Zero(t, balance)
}

func TestBindingCannotExpireWhileWaitingForLocks(t *testing.T) {
	for _, flow := range []string{"website", "email"} {
		for _, lock := range []string{"app", "account"} {
			t.Run(flow+"_"+lock, func(t *testing.T) {
				s, mail, c, e, uid := emailFixture(t)
				db := s.db
				ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
				defer cancel()
				token, table := "", "qq_bot_bind_requests"
				if flow == "website" {
					code, err := s.CreateBindingCode(ctx, uid)
					require.NoError(t, err)
					require.Contains(t, s.claimBinding(ctx, c, e, code.Code), "已受理")
				} else {
					require.Equal(t, emailBindingAccepted, requestBindingEmail(s, ctx, c, e, "Alice@example.com"))
					token, table = mail.token(t), "qq_bot_email_bind_requests"
				}
				expiresAt := time.Now().Add(500 * time.Millisecond)
				_, err := db.Exec(`UPDATE `+table+` SET expires_at=$1`, expiresAt)
				require.NoError(t, err)
				blocker, err := db.BeginTx(ctx, nil)
				require.NoError(t, err)
				defer func() { _ = blocker.Rollback() }()
				var pid int
				require.NoError(t, blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
				if lock == "app" {
					_, err = blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,273))`, c.AppID)
				} else {
					_, err = blocker.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR NO KEY UPDATE`, uid)
				}
				require.NoError(t, err)
				done := make(chan error, 1)
				go func() {
					if flow == "website" {
						done <- s.ConfirmBinding(ctx, uid, e.Author.MemberID)
					} else {
						_, err := s.ConfirmEmailBinding(ctx, token, e.Author.MemberID)
						done <- err
					}
				}()
				waitForDatabaseBlocker(t, db, pid)
				require.Eventually(t, func() bool { return time.Now().After(expiresAt) }, 2*time.Second, 10*time.Millisecond)
				require.NoError(t, blocker.Commit())
				require.Error(t, <-done, "expired requests must not bind after a lock wait")
				var count int
				require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_bindings`).Scan(&count))
				require.Zero(t, count)
			})
		}
	}
}

func TestInviteSettlementSkipsIneligibleHistory(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	inviter := user(t, db)
	bind(t, db, inviter, "inviter")
	// Pending, unbound relationships must not exhaust the settlement batch.
	_, err := db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at)
 SELECT 'app','group','unbound-'||n,'inviter',NOW()-INTERVAL '1 minute',NOW() FROM generate_series(1,105) n`)
	require.NoError(t, err)
	bind(t, db, user(t, db), "eligible")
	_, err = db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at)
 VALUES('app','group','eligible','inviter',NOW()-INTERVAL '1 minute',NOW())`)
	require.NoError(t, err)
	require.NoError(t, s.SettleInvites(ctx, "inviter"))
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, 1.0, balance)
	require.NoError(t, s.SettleInvites(ctx, "inviter"))
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, 1.0, balance)
}

func TestInvitationReentryCannotTransferOrRepeatReward(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	first, second, invited := user(t, db), user(t, db), user(t, db)
	bind(t, db, first, "first")
	bind(t, db, second, "second")
	bind(t, db, invited, "invitee")
	now := time.Now().Unix()
	e := Event{GroupID: "group", MemberID: "invitee", ApplySource: "invited", InvitedBy: "first", ApplyAt: time.Unix(now-60, 0).Format(time.RFC3339)}
	j := Job{AppID: c.AppID, GroupID: e.GroupID, MemberID: e.MemberID, EventType: "GROUP_JOIN_REQUEST"}
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	e.Timestamp = json.RawMessage(strconv.FormatInt(now, 10))
	j.EventType = "GROUP_MEMBER_ADD"
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	j.EventType = "GROUP_MEMBER_REMOVE"
	e.Timestamp = json.RawMessage(strconv.FormatInt(now+10, 10))
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	for _, group := range []string{"group", "other-group"} {
		e.GroupID, j.GroupID = group, group
		e.InvitedBy = "second"
		e.ApplyAt = time.Unix(now+20, 0).Format(time.RFC3339)
		j.EventType = "GROUP_JOIN_REQUEST"
		require.NoError(t, s.memberEvent(ctx, c, j, e))
		j.EventType = "GROUP_MEMBER_ADD"
		e.Timestamp = json.RawMessage(strconv.FormatInt(now+30, 10))
		require.NoError(t, s.memberEvent(ctx, c, j, e))
	}
	// Rebinding the original website account to another QQ cannot earn again.
	require.NoError(t, s.Unbind(ctx, invited, observedBinding(t, s, invited, "invitee")))
	bind(t, db, invited, "another-qq")
	_, err = db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at)
 VALUES('app','group','another-qq','second',NOW()-INTERVAL '1 minute',NOW())`)
	require.NoError(t, err)
	_, err = s.reward(ctx, c, Job{AppID: c.AppID, GroupID: "group", MemberID: "another-qq"}, "invite")
	require.NoError(t, err)
	var count int
	var firstBalance, secondBalance float64
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE kind='invite'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, first).Scan(&firstBalance))
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, second).Scan(&secondBalance))
	require.Equal(t, c.InviteReward, firstBalance)
	require.Zero(t, secondBalance)
}

func TestRewardUsesCurrentAuthorizationAndInvitationRules(t *testing.T) {
	for _, change := range []string{"bot_disabled", "group_removed", "invite_disabled", "amount_changed", "limit_lowered"} {
		t.Run(change, func(t *testing.T) {
			db := testDB(t)
			s := savedBot(t, db)
			ctx := context.Background()
			stale, err := s.Config(ctx)
			require.NoError(t, err)
			inviter, invitee := user(t, db), user(t, db)
			bind(t, db, inviter, "inviter")
			bind(t, db, invitee, "invitee")
			_, err = db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at)
 VALUES('app','group','invitee','inviter',NOW()-INTERVAL '1 minute',NOW())`)
			require.NoError(t, err)
			current := stale
			switch change {
			case "bot_disabled":
				current.Enabled = false
			case "group_removed":
				current.AllowedGroups = []string{"other-group"}
			case "invite_disabled":
				current.InviteReward = 0
			case "amount_changed":
				current.InviteReward = .25
			case "limit_lowered":
				current.InviteDailyLimit = 0
			}
			_, err = s.SaveConfig(ctx, current)
			require.NoError(t, err)
			_, err = s.reward(ctx, stale, Job{AppID: stale.AppID, GroupID: "group", MemberID: "invitee"}, "invite")
			require.NoError(t, err)
			var balance float64
			require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
			want := 0.0
			if change == "amount_changed" {
				want = .25
			}
			require.Equal(t, want, balance)
			if change == "bot_disabled" || change == "group_removed" {
				_, err = s.reward(ctx, stale, Job{AppID: stale.AppID, GroupID: "group", MemberID: "invitee"}, "checkin")
				require.NoError(t, err)
				require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, invitee).Scan(&balance))
				require.Zero(t, balance)
			}
		})
	}
}

func TestUsageReservationHonorsCurrentShutdownAndLimits(t *testing.T) {
	for _, change := range []string{"bot_disabled", "group_removed", "chat_limit_zero", "image_disabled"} {
		t.Run(change, func(t *testing.T) {
			db := testDB(t)
			s := savedBot(t, db)
			ctx := context.Background()
			stale, err := s.Config(ctx)
			require.NoError(t, err)
			stale.ImageEnabled, stale.ImageModel = true, "test-image"
			_, err = s.SaveConfig(ctx, stale)
			require.NoError(t, err)
			current, kind := stale, "chat"
			switch change {
			case "bot_disabled":
				current.Enabled = false
			case "group_removed":
				current.AllowedGroups = []string{"other-group"}
			case "chat_limit_zero":
				current.TotalDailyChatLimit = 0
			case "image_disabled":
				current.ImageEnabled, kind = false, "image"
			}
			_, err = s.SaveConfig(ctx, current)
			require.NoError(t, err)
			require.Error(t, s.reserveUsage(ctx, stale, Job{AppID: stale.AppID, ID: "pending", GroupID: "group", MemberID: "member"}, kind))
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_daily_usage`).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestWebsiteBindingRejectsUnavailableAccount(t *testing.T) {
	for _, state := range []string{"disabled", "deleted"} {
		t.Run(state, func(t *testing.T) {
			db := testDB(t)
			s := savedBot(t, db)
			ctx := context.Background()
			uid := user(t, db)
			c, err := s.Config(ctx)
			require.NoError(t, err)
			code, err := s.CreateBindingCode(ctx, uid)
			require.NoError(t, err)
			e := Event{GroupID: "group"}
			e.Author.MemberID = "member"
			require.Contains(t, s.claimBinding(ctx, c, e, code.Code), "已受理")
			query := `UPDATE users SET status='disabled' WHERE id=$1`
			if state == "deleted" {
				query = `UPDATE users SET deleted_at=NOW() WHERE id=$1`
			}
			_, err = db.Exec(query, uid)
			require.NoError(t, err)
			require.Error(t, s.ConfirmBinding(ctx, uid, "member"))
		})
	}
}
