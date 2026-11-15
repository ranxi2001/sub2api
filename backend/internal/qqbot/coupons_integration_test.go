//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/coupon"
	"github.com/stretchr/testify/require"
)

func couponFixture(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(`INSERT INTO recharge_coupon_templates(name,kind,currency,min_amount,value,valid_days) VALUES('满100减10','fixed','CNY',100,10,7) RETURNING id`).Scan(&id)
	require.NoError(t, err)
	return id
}
func TestCouponCheckinGrowthAndWallet(t *testing.T) {
	cmd, arg := command("我的优惠券 2")
	require.Equal(t, "我的优惠券", cmd)
	require.Equal(t, "2", arg)
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	tid := couponFixture(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Enabled: true, CouponTemplateID: tid, CouponQuantity: 1, StreakDays: 1, StreakReward: &GrowthReward{Type: "coupon", CouponTemplateID: tid, Quantity: 1}, Growth: GrowthConfig{Tasks: []GrowthTask{{ID: "bind_coupon", Name: "绑定送券", Kind: "binding", Target: 1, Enabled: true, Reward: &GrowthReward{Type: "coupon", CouponTemplateID: tid, Quantity: 2}}}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	j := Job{GroupID: "group", MemberID: "member"}
	result, err := s.reward(ctx, c, j, "checkin")
	require.NoError(t, err)
	require.Contains(t, result, "优惠券")
	result, err = s.reward(ctx, c, j, "checkin")
	require.NoError(t, err)
	require.Contains(t, result, "今日已签到")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_recharge_coupons WHERE user_id=$1`, uid).Scan(&count))
	require.Equal(t, 4, count)
	records, err := s.ListRewardCodes(ctx, "", "", 1)
	require.NoError(t, err)
	require.Len(t, records.Items, 3)
	for _, r := range records.Items {
		require.Equal(t, "coupon", r.RewardType)
		require.Equal(t, tid, r.CouponTemplateID)
		require.Equal(t, "fulfilled", r.Status)
	}
	wallet, err := s.myCoupons(ctx, c, j, "")
	require.NoError(t, err)
	require.Contains(t, wallet, "满100减10")
	require.Contains(t, wallet, "不可叠加充值阶梯优惠")
	history, err := s.ListActivityRecords(ctx, "checkin", "", 1, 20)
	require.NoError(t, err)
	require.Len(t, history.Items, 1)
	require.Equal(t, 1, history.Items[0].CouponQuantity)
	require.Equal(t, tid, history.Items[0].CouponTemplateID)
	totals, err := s.myRewards(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, totals, "累计 4 张，当前可用 4 张")
	require.Contains(t, siteCheckinRules(cfg), "充值优惠券 × 1")
	preview, err := s.PreviewActivities(ctx, uid)
	require.NoError(t, err)
	require.Contains(t, preview.CheckinRule, "充值优惠券 × 1")
	other := user(t, db)
	bind(t, db, other, "other")
	wallet, err = s.myCoupons(ctx, c, Job{MemberID: "other"}, "")
	require.NoError(t, err)
	require.NotContains(t, wallet, "满100减10")
	_, err = db.Exec(`UPDATE recharge_coupon_templates SET enabled=false WHERE id=$1`, tid)
	require.NoError(t, err)
	require.Error(t, s.SaveCheckinConfig(ctx, cfg))
	cfg.Enabled = false
	for i := range cfg.Growth.Tasks {
		cfg.Growth.Tasks[i].Enabled = false
	}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg), "disabled activities must remain stoppable after template suspension")
}
func TestCouponOnlyInviteIdempotency(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	tid := couponFixture(t, db)
	require.NoError(t, s.SaveInviteConfig(ctx, InviteConfig{Enabled: true, InviteDailyLimit: 10, CouponTemplateID: tid, CouponQuantity: 2}))
	c, err := s.Config(ctx)
	require.NoError(t, err)
	inviter, invitee := user(t, db), user(t, db)
	bind(t, db, inviter, "inviter")
	bind(t, db, invitee, "invitee")
	e := Event{GroupID: "group", MemberID: "invitee", InvitedBy: "inviter", ApplySource: "invited", ApplyAt: time.Now().Add(-time.Minute).Format(time.RFC3339), Timestamp: json.RawMessage(strconv.FormatInt(time.Now().Unix(), 10))}
	j := Job{GroupID: "group", MemberID: "invitee", EventType: "GROUP_JOIN_REQUEST"}
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	j.EventType = "GROUP_MEMBER_ADD"
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	_, err = s.reward(ctx, c, j, "invite")
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_recharge_coupons WHERE user_id=$1`, inviter).Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_recharge_coupons WHERE user_id=$1`, invitee).Scan(&count))
	require.Zero(t, count)
	info, err := s.inviteInfo(ctx, c, Job{MemberID: "inviter", GroupID: "group"})
	require.NoError(t, err)
	require.Contains(t, info, "充值优惠券 × 2")
	history, err := s.ListActivityRecords(ctx, "invite", "", 1, 20)
	require.NoError(t, err)
	require.Len(t, history.Items, 1)
	require.Equal(t, "rewarded", history.Items[0].Status)
	require.Equal(t, 2, history.Items[0].CouponQuantity)
	require.Equal(t, tid, history.Items[0].CouponTemplateID)
	_, err = db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at) VALUES('app','other-group','invitee','other-inviter',NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute')`)
	require.NoError(t, err)
	history, err = s.ListActivityRecords(ctx, "invite", "other-group", 1, 20)
	require.NoError(t, err)
	require.Len(t, history.Items, 1)
	require.Equal(t, "already_rewarded", history.Items[0].Status)
	require.Zero(t, history.Items[0].CouponQuantity)
	require.Zero(t, history.Items[0].CouponTemplateID)

}
func TestCouponLotteryAtomicReplayAndCap(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, cfg := lotteryFixture(t, 100)
	tid := couponFixture(t, db)
	cfg.Prizes = []LotteryPrize{{Name: "coupon", CouponTemplateID: tid, CouponQuantity: 2, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	job := Job{ID: "coupon-draw", GroupID: "group", MemberID: "member"}
	text, err := s.drawLotteryCount(ctx, c, job, 2)
	require.NoError(t, err)
	require.Contains(t, text, "优惠券")
	replay, err := s.drawLotteryCount(ctx, c, job, 2)
	require.NoError(t, err)
	require.Equal(t, text, replay)
	history, err := s.ListActivityRecords(ctx, "lottery", "", 1, 20)
	require.NoError(t, err)
	require.Len(t, history.Items, 2)
	for _, r := range history.Items {
		require.Equal(t, "credited", r.Status)
		require.Equal(t, 2, r.CouponQuantity)
		require.Equal(t, tid, r.CouponTemplateID)
	}
	draws, err := s.ListLotteryDraws(ctx, "", 1, 20)
	require.NoError(t, err)
	require.Len(t, draws.Items, 2)
	require.Equal(t, 2, draws.Items[0].CouponQuantity)
	require.Equal(t, "充值优惠券 × 2", lotteryPrizeDescription(cfg.Prizes[0]))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_recharge_coupons WHERE user_id=$1`, uid).Scan(&count))
	require.Equal(t, 4, count)
	_, err = db.Exec(`UPDATE recharge_coupon_templates SET max_issues=5 WHERE id=$1`, tid)
	require.NoError(t, err)
	_, err = s.drawLottery(ctx, c, Job{ID: "exceeds-cap", GroupID: "group", MemberID: "member"})
	require.Error(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_recharge_coupons WHERE user_id=$1`, uid).Scan(&count))
	require.Equal(t, 4, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_lottery_draws WHERE user_id=$1`, uid).Scan(&count))
	require.Equal(t, 2, count)
}

func TestCouponRankingAndAdminBatchConcurrency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db := testDB(t)
	s := savedBot(t, db)
	insightsUsageTable(t, db)
	a, b := user(t, db), user(t, db)
	tid := couponFixture(t, db)
	start := beijingDayStart(time.Now()).AddDate(0, 0, -1)
	cfg := defaultRankingConfig()
	cfg.RewardsEnabled = true
	cfg.TokenRewards = []RankingPrize{{Rank: 1, Reward: GrowthReward{Type: "coupon", CouponTemplateID: tid, Quantity: 1}}, {Rank: 2, Reward: GrowthReward{Type: "coupon", CouponTemplateID: tid, Quantity: 2}}}
	cfg.AmountRewards = []RankingPrize{}
	_, err := s.saveRankingConfigAt(ctx, cfg, start.Add(-time.Hour))
	require.NoError(t, err)
	rankingUsage(t, db, a, 1000, "10", start.Add(time.Hour))
	rankingUsage(t, db, b, 2000, "10", start.Add(time.Hour))
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errs <- s.settleDueRankings(ctx, start.AddDate(0, 0, 1).Add(time.Minute)) }()
	go func() {
		defer wg.Done()
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			errs <- e
			return
		}
		defer tx.Rollback()
		for _, uid := range []int64{a, b} {
			if _, e = tx.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, uid); e != nil {
				errs <- e
				return
			}
		}
		for _, uid := range []int64{a, b} {
			if _, e = coupon.Issue(ctx, tx, tid, uid, 1, "admin", fmt.Sprintf("batch:%d", uid), time.Now()); e != nil {
				errs <- e
				return
			}
		}
		errs <- tx.Commit()
	}()
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	require.NoError(t, s.settleDueRankings(ctx, start.AddDate(0, 0, 1).Add(time.Hour)))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_recharge_coupons`).Scan(&count))
	require.Equal(t, 5, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_recharge_coupons WHERE user_id=$1 AND source='qq_ranking'`, a).Scan(&count))
	require.Equal(t, 2, count)
}

func TestCouponPeriodicMilestoneAndGuaranteedRewards(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, lottery := lotteryFixture(t, 100)
	insightsUsageTable(t, db)
	tid := couponFixture(t, db)
	reward := GrowthReward{Type: "coupon", CouponTemplateID: tid, Quantity: 1}
	cfg := CheckinConfig{Enabled: true, Milestones: []CheckinMilestone{{ID: "coupon_day", Name: "签到送券", Days: 1, Enabled: true, Reward: reward}}, Growth: GrowthConfig{Tasks: []GrowthTask{}, PeriodicTasks: []GrowthTask{{ID: "coupon_spend", Name: "消费送券", Kind: "usage_amount", Target: 10, Enabled: true, Reward: &reward}}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	_, err := s.reward(ctx, c, Job{GroupID: "group", MemberID: "member"}, "checkin")
	require.NoError(t, err)
	rankingUsage(t, db, uid, 1000, "25", time.Now().Add(-time.Minute))
	_, n, err := s.processGrowthTasks(ctx, c, Job{GroupID: "group", MemberID: "member"}, time.Now(), true)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	_, n, err = s.processGrowthTasks(ctx, c, Job{GroupID: "group", MemberID: "member"}, time.Now(), true)
	require.NoError(t, err)
	require.Zero(t, n)
	lottery.GuaranteeEnabled, lottery.GuaranteeEvery = true, 1
	lottery.GuaranteePrizes = []LotteryPrize{{Name: "保底优惠券", CouponTemplateID: tid, CouponQuantity: 2, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, lottery))
	text, err := s.drawLottery(ctx, c, Job{ID: "coupon-guarantee", GroupID: "group", MemberID: "member"})
	require.NoError(t, err)
	require.Contains(t, text, "保底优惠券")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_recharge_coupons WHERE user_id=$1`, uid).Scan(&count))
	require.Equal(t, 5, count)
	records, err := s.ListLotteryDraws(ctx, "", 1, 20)
	require.NoError(t, err)
	require.True(t, records.Items[0].IsGuaranteed)
	require.Equal(t, 2, records.Items[0].CouponQuantity)
}
