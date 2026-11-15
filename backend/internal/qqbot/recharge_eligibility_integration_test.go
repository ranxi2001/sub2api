//go:build integration

package qqbot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCheckinRechargeThresholdsRejectWithoutConsumingDay(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{RewardDistribution: &CheckinRewardDistribution{7000, 2000, 1000}, Enabled: true, MinAmount: .1, MaxAmount: .1, StreakDays: 7, Growth: GrowthConfig{Tasks: (GrowthConfig{}).tasks()},
		RechargeRequirement: RechargeRequirement{MinRechargeTotal: 25, MinRecharge30d: 10, RechargeWindowDays: 7}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	got, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, normalizeCheckinMilestones(cfg), got)
	lifetime, recent := user(t, db), user(t, db)
	bind(t, db, lifetime, "lifetime")
	bind(t, db, recent, "recent")
	_, err = db.Exec(`INSERT INTO payment_orders(user_id,amount,bonus_amount,paid_at,completed_at)
 VALUES($1,30,5,NOW()-INTERVAL '45 days',NOW()-INTERVAL '45 days'),
 ($2,25,5,NOW()-INTERVAL '45 days',NOW()-INTERVAL '45 days')`, lifetime, recent)
	require.NoError(t, err)
	text, err := s.reward(ctx, c, Job{GroupID: "group", MemberID: "lifetime"}, "checkin")
	require.NoError(t, err)
	require.Contains(t, text, "签到成功", "historical principal alone can satisfy lifetime threshold")
	cfg.MinRechargeTotal = 100
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	j := Job{GroupID: "group", MemberID: "recent"}
	text, err = s.reward(ctx, c, j, "checkin")
	require.NoError(t, err)
	require.Contains(t, text, "签到暂不满足：")
	require.Contains(t, text, "累计有效充值满 USD 100.00 或 近 7 天有效充值满 USD 10.00")
	require.Contains(t, text, "累计充值：USD 20.00\n近 7 天充值：USD 0.00")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE user_id=$1`, recent).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`INSERT INTO payment_orders(user_id,amount,completed_at) VALUES($1,10,NOW()-INTERVAL '8 days')`, recent)
	require.NoError(t, err)
	text, err = s.reward(ctx, c, j, "checkin")
	require.NoError(t, err)
	require.Contains(t, text, "签到暂不满足：", "an eight-day-old recharge does not qualify for a seven-day window")
	cfg.RechargeWindowDays = 10
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	text, err = s.reward(ctx, c, j, "checkin")
	require.NoError(t, err)
	require.Contains(t, text, "签到成功", "expanding the window qualifies the same recharge without consuming the earlier attempt")
	text, err = s.reward(ctx, c, j, "checkin")
	require.NoError(t, err)
	require.Equal(t, "今日已签到。", text)
}

func TestRechargeEligibilityUsesPrincipalCodesAndRollingWindow(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	uid := user(t, db)
	_, err := db.Exec(`INSERT INTO payment_orders(user_id,amount,bonus_amount,refund_amount,status,completed_at,recharge_code) VALUES
 ($1,100,0,0,'COMPLETED',NOW()-INTERVAL '31 days','old'),
 ($1,25,5,0,'COMPLETED',NOW(),'online'),
 ($1,4,0,0,'COMPLETED',NOW()-INTERVAL '20 days','twenty-days'),
 ($1,20,4,10,'PARTIALLY_REFUNDED',NOW(),'partial'),
 ($1,99,0,99,'REFUNDING',NOW(),'refunding'),
 ($1,99,0,0,'PENDING',NULL,'pending')`, uid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at) VALUES
 ('recent','balance',3,'used',$1,NOW()),('twenty-code','balance',6,'used',$1,NOW()-INTERVAL '20 days'),('old-code','balance',2,'used',$1,NOW()-INTERVAL '31 days'),
 ('online','balance',25,'used',$1,NOW()),('refunding','balance',99,'used',$1,NOW()),
 ('pending','balance',99,'used',$1,NOW()),('gift','admin_balance',100,'used',$1,NOW()),
 ('unused','balance',100,'unused',$1,NULL)`, uid)
	require.NoError(t, err)
	for _, tc := range []struct {
		days   int
		recent string
	}{{0, "41"}, {1, "31"}, {7, "31"}, {30, "41"}, {60, "143"}, {3650, "143"}} {
		e, err := (RechargeRequirement{RechargeWindowDays: tc.days}).eligibility(ctx, db, uid, false)
		require.NoError(t, err)
		require.Equal(t, "143", e.Total.String())
		require.Equal(t, tc.recent, e.Recent.String(), "window: %d days", tc.days)
	}
}

func TestInviteRechargeChecksInviterAndReportsConditions(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	cfg := InviteConfig{Enabled: true, InviteReward: 1.25, InviteDailyLimit: 10,
		RechargeRequirement: RechargeRequirement{MinRechargeTotal: 50, MinRecharge30d: 10, RechargeWindowDays: 60}}
	require.NoError(t, s.SaveInviteConfig(ctx, cfg))
	c, err := s.Config(ctx)
	require.NoError(t, err)
	inviter, invitee := user(t, db), user(t, db)
	bind(t, db, inviter, "inviter")
	bind(t, db, invitee, "invitee")
	_, err = db.Exec(`INSERT INTO payment_orders(user_id,amount) VALUES($1,100)`, invitee)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at)
 VALUES('app','group','invitee','inviter',NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute')`)
	require.NoError(t, err)
	text, err := s.reward(ctx, c, Job{GroupID: "group", MemberID: "invitee"}, "invite")
	require.NoError(t, err)
	require.Contains(t, text, "邀请奖励暂不满足：", "invitee recharge must not qualify the inviter")
	require.NoError(t, s.SettleInvites(ctx, "inviter"))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards`).Scan(&count))
	require.Zero(t, count)
	page, err := s.ListActivityRecords(ctx, "invite", "", 1, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "recharge_required", page.Items[0].Status)
	info, err := s.inviteInfo(ctx, c, Job{MemberID: "inviter"})
	require.NoError(t, err)
	require.Contains(t, info, "累计有效充值满 USD 50.00 或 近 60 天有效充值满 USD 10.00")
	require.Contains(t, info, "邀请人账号")
	require.Contains(t, info, "充值资格：未满足")
	info, err = s.inviteInfo(ctx, c, Job{MemberID: "unbound"})
	require.NoError(t, err)
	require.Contains(t, info, "累计有效充值满 USD 50.00")
	require.Contains(t, info, "请先发送“绑定 邮箱或绑定码”")
	require.Contains(t, info, "近 60 天有效充值满 USD 10.00")
	_, err = db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at) VALUES('qualified','balance',10,'used',$1,NOW()-INTERVAL '45 days')`, inviter)
	require.NoError(t, err)
	page, err = s.ListActivityRecords(ctx, "invite", "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, "pending_reward", page.Items[0].Status, "admin records use the configured 60-day window")
	cfg.RechargeWindowDays = 7
	require.NoError(t, s.SaveInviteConfig(ctx, cfg))
	page, err = s.ListActivityRecords(ctx, "invite", "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, "recharge_required", page.Items[0].Status, "shortening the window updates the qualification status")
	cfg.RechargeWindowDays = 60
	require.NoError(t, s.SaveInviteConfig(ctx, cfg))
	for range 2 {
		require.NoError(t, s.SettleInvites(ctx, "inviter"))
	}
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards`).Scan(&count))
	require.Equal(t, 1, count)
	page, err = s.ListActivityRecords(ctx, "invite", "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, "rewarded", page.Items[0].Status)
	info, err = s.inviteInfo(ctx, c, Job{MemberID: "inviter"})
	require.NoError(t, err)
	require.Contains(t, info, "充值资格：已满足")
	require.Contains(t, info, "近 60 天充值：USD 10.00")
	require.Contains(t, info, "今日已奖励：1/10 人")
}

func TestCheckinRechargeEligibilityWaitsForRefund(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	_, err := db.Exec(`INSERT INTO payment_orders(user_id,amount) VALUES($1,10)`, uid)
	require.NoError(t, err)
	require.NoError(t, s.SaveCheckinConfig(ctx, CheckinConfig{Enabled: true, MinAmount: 1, MaxAmount: 1,
		RechargeRequirement: RechargeRequirement{MinRechargeTotal: 10}}))
	c, err := s.Config(ctx)
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE payment_orders SET status='REFUNDING' WHERE user_id=$1`, uid)
	require.NoError(t, err)
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := s.reward(ctx, c, Job{GroupID: "group", MemberID: "member"}, "checkin")
		done <- result{text, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("reward ignored locked refund: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.Contains(t, r.text, "签到暂不满足：")
	case <-time.After(5 * time.Second):
		t.Fatal("reward did not resume")
	}
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, balance)
}

func TestLegacyRechargeWindowsAndIndependentActivitySettings(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	_, err := db.Exec("UPDATE settings SET value=(value::jsonb - 'invite_recharge_window_days')::text WHERE key=$1", configKey)
	require.NoError(t, err)
	// Seed the legacy persisted shape directly so omitted fields remain absent.
	_, err = db.Exec(`INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW())
 ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=NOW()`, checkinConfigKey,
		`{"enabled":true,"min_amount":0.1,"max_amount":0.1,"min_recharge_30d":5}`)
	require.NoError(t, err)
	checkin, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 30, checkin.RechargeWindowDays)
	require.Equal(t, float64(5), checkin.MinRecharge30d)
	invite, err := s.InviteConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 30, invite.RechargeWindowDays)
	checkin.RechargeWindowDays = 7
	invite.RechargeWindowDays = 60
	require.NoError(t, s.SaveCheckinConfig(ctx, checkin))
	require.NoError(t, s.SaveInviteConfig(ctx, invite))
	gotCheckin, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, checkin, gotCheckin)
	gotInvite, err := s.InviteConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, invite, gotInvite)
}
