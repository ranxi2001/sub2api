//go:build integration

package qqbot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGrowthCustomTargetsAndRetainedClaims(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid, other := user(t, db), user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	j := Job{GroupID: "group", MemberID: "member"}
	require.NoError(t, s.SaveCheckinConfig(ctx, CheckinConfig{Growth: GrowthConfig{BindingChances: 2}}))
	text, err := s.growthTasks(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, text, "2 次")
	cfg, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	cfg.Enabled = true
	cfg.Growth.Tasks[0].Name = "Renamed binding"
	cfg.Growth.Tasks[0].Chances = 9
	cfg.Growth.Tasks = append(cfg.Growth.Tasks,
		GrowthTask{ID: "api_3", Name: "API three", Kind: "usage_count", Target: 3, Chances: 3, Enabled: true},
		GrowthTask{ID: "days_2", Name: "Two days", Kind: "checkin_total", Target: 2, Chances: 4, Enabled: true},
		GrowthTask{ID: "streak_2", Name: "Two in a row", Kind: "checkin_streak", Target: 2, Chances: 5, Enabled: true},
		GrowthTask{ID: "recharge_25", Name: "Recharge 25", Kind: "recharge_total", Target: 25, Chances: 6, Enabled: true},
		GrowthTask{ID: "disabled", Name: "Disabled task", Kind: "binding", Target: 1, Chances: 100},
	)
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,created_at) VALUES($1,'test',NOW()),($1,'test',NOW()),($1,'future',NOW()+INTERVAL '1 day'),($2,'other',NOW())`, uid, other)
	require.NoError(t, err)
	for i := 1; i <= 2; i++ {
		seedCheckin(t, db, uid, time.Now().AddDate(0, 0, -i))
	}
	_, err = db.Exec(`INSERT INTO payment_orders(user_id,amount,bonus_amount,refund_amount,status,recharge_code) VALUES($1,40,10,20,'PARTIALLY_REFUNDED','order-linked'),($1,100,0,100,'REFUNDED',''),($2,200,0,0,'COMPLETED','')`, uid, other)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO payment_orders(user_id,order_type,amount) VALUES($1,'subscription',100)`, uid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at) VALUES('order-linked','balance',40,'used',$1,NOW()),('offline','balance',5,'used',$1,NOW()),('admin','admin_balance',100,'used',$1,NOW())`, uid)
	require.NoError(t, err)
	text, err = s.growthTasks(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, text, "2/3 次")
	require.Contains(t, text, "USD 20.00/25.00")
	require.Contains(t, text, "已发放")
	require.Contains(t, text, "未开启")
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,created_at) VALUES($1,'test',NOW())`, uid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at) VALUES('extra','balance',5,'used',$1,NOW())`, uid)
	require.NoError(t, err)
	text, err = s.growthTasks(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, text, "已自动发放")
	var granted int
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, uid).Scan(&granted))
	require.Equal(t, 20, granted)
	// Reordering and editing a task preserve its original grant key.
	claimed := cfg.Growth.Tasks[2] // api_3; sign-in tasks are now separate milestones.
	claimed.Name, claimed.Target, claimed.Chances = "Edited milestone", 1, 1000
	cfg.Growth.Tasks = []GrowthTask{claimed}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	text, err = s.growthTasks(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, text, "已发放")
	cfg.Growth.Tasks = []GrowthTask{}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	loaded, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	require.Empty(t, loaded.Growth.Tasks)
	text, err = s.growthTasks(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, text, "暂无任务")
}

func TestGrowthCustomStreakInterval(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	c, err := s.Config(ctx)
	require.NoError(t, err)
	require.NoError(t, s.SaveCheckinConfig(ctx, CheckinConfig{Enabled: true, StreakDays: 3, StreakChances: 2}))
	for i := 1; i <= 2; i++ {
		seedCheckin(t, db, uid, time.Now().AddDate(0, 0, -i))
	}
	text, err := s.reward(ctx, c, Job{GroupID: "group", MemberID: "member"}, "checkin")
	require.NoError(t, err)
	require.Contains(t, text, "连签 3 天")
	require.Contains(t, text, "连签奖励：2 次")
	require.Contains(t, text, "距下次奖励 3 天")
}
