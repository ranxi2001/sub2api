//go:build integration

package qqbot

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestGrowthTypedRewardsAtomicAndConcurrent(t *testing.T) {
	for _, kind := range []string{"balance", "chances", "physical", "other"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			db := testDB(t)
			s := savedBot(t, db)
			uid := user(t, db)
			bind(t, db, uid, "member")
			c, err := s.Config(ctx)
			require.NoError(t, err)
			reward := GrowthReward{Type: kind, Quantity: 2}
			if kind == "balance" {
				reward.Quantity = 0
				reward.Amount = 1.23456789
			}
			if kind == "physical" || kind == "other" {
				reward.PrizeName = "Reward item"
				reward.ClaimInstructions = "Contact admin"
			}
			cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{{ID: "typed", Name: "Typed reward", Kind: "binding", Target: 1, Enabled: true, Reward: &reward}}}}
			require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
			var wins, failures atomic.Int32
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, grants, e := s.processGrowthTasks(ctx, c, Job{GroupID: "group", MemberID: "member"}, time.Now(), true)
					if e != nil {
						failures.Add(1)
					}
					if grants == 1 {
						wins.Add(1)
					}
				}()
			}
			wg.Wait()
			require.Zero(t, failures.Load())
			require.EqualValues(t, 1, wins.Load())
			records, err := s.ListRewardCodes(ctx, "", "", 1)
			require.NoError(t, err)
			require.Len(t, records.Items, 1)
			row := records.Items[0]
			require.Equal(t, "growth", row.SourceKind)
			require.Empty(t, row.CodeHint)
			require.Equal(t, kind, row.RewardType)
			var balance float64
			var chances, history, ledger int
			require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
			require.NoError(t, db.QueryRow(`SELECT COALESCE(SUM(chances),0) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, uid).Scan(&chances))
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM redeem_codes WHERE used_by=$1`, uid).Scan(&history))
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE user_id=$1`, uid).Scan(&ledger))
			switch kind {
			case "balance":
				require.InDelta(t, 1.23456789, balance, 1e-9)
				require.Equal(t, 1, history)
				require.Equal(t, 1, ledger)
				require.Zero(t, chances)
			case "chances":
				require.Equal(t, 2, chances)
				require.Zero(t, balance)
				require.Zero(t, history)
			default:
				require.Zero(t, balance)
				require.Zero(t, chances)
				require.Equal(t, "pending", row.Status)
				reply, err := s.growthTasks(ctx, c, Job{GroupID: "group", MemberID: "member"})
				require.NoError(t, err)
				require.Contains(t, reply, "待管理员发放")
				require.NoError(t, s.UpdateRewardCode(ctx, row.ID, "fulfill", "Delivered"))
				require.ErrorIs(t, s.UpdateRewardCode(ctx, row.ID, "fulfill", "again"), ErrRewardCodeRequest)
			}
			// Rebinding and changing the reward type must not reset the entitlement.
			_, err = db.Exec(`UPDATE qq_bot_bindings SET member_id='rebound' WHERE user_id=$1`, uid)
			require.NoError(t, err)
			cfg.Growth.Tasks[0].Reward = &GrowthReward{Type: "balance", Amount: 10}
			require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
			reply, err := s.growthTasks(ctx, c, Job{GroupID: "other-group", MemberID: "rebound"})
			require.NoError(t, err)
			require.Contains(t, reply, "已发放")
			require.Contains(t, reply, reward.description())
			require.NotContains(t, reply, "余额 USD 10")
			raw, err := migrations.FS.ReadFile("361_qq_bot_growth_rewards.sql")
			require.NoError(t, err)
			_, err = db.Exec(string(raw))
			require.NoError(t, err)
		})
	}
}

func TestGrowthUsageTokensAndActualSpending(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	other := user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{
		{ID: "tokens", Name: "Tokens", Kind: "usage_tokens", Target: 1000, Enabled: true, Reward: &GrowthReward{Type: "balance", Amount: 1.25}},
		{ID: "spend", Name: "Spend", Kind: "usage_amount", Target: 10, Enabled: true, Reward: &GrowthReward{Type: "physical", Quantity: 1, PrizeName: "cup"}},
	}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,actual_cost,billing_type,created_at) VALUES($1,'past',600,200,100,99,9.9999999999,1,NOW()),($1,'future',10000,0,0,0,100,0,NOW()+INTERVAL '1 day'),($2,'other',10000,0,0,0,100,0,NOW())`, uid, other)
	require.NoError(t, err)
	j := Job{GroupID: "group", MemberID: "member"}
	text, err := s.growthTasks(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, text, "999/1000 Token")
	require.Contains(t, text, "USD 9.99/10.00")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,cache_read_tokens,actual_cost,billing_type,created_at) VALUES($1,'new',1,0.0000000001,0,NOW())`, uid)
	require.NoError(t, err)
	text, err = s.growthTasks(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, text, "已自动发放")
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&count))
	require.Equal(t, 2, count)
	text, err = s.myRewards(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, text, "成长：USD 1.25")
}

func TestStreakTypedRewardsAndLegacyClaims(t *testing.T) {
	for _, kind := range []string{"balance", "physical", "other"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			db := testDB(t)
			s := savedBot(t, db)
			uid := user(t, db)
			bind(t, db, uid, "member")
			c, err := s.Config(ctx)
			require.NoError(t, err)
			reward := GrowthReward{Type: kind, Quantity: 1, PrizeName: "cup", ClaimInstructions: "联系管理员领取奖品"}
			if kind == "balance" {
				reward.Amount = 2
				reward.Quantity = 0
			}
			require.NoError(t, s.SaveCheckinConfig(ctx, CheckinConfig{Enabled: true, MinAmount: .5, MaxAmount: .5, StreakDays: 7, StreakReward: &reward}))
			for i := 1; i < 7; i++ {
				seedCheckin(t, db, uid, time.Now().AddDate(0, 0, -i))
			}
			j := Job{GroupID: "group", MemberID: "member"}
			reply, err := s.reward(ctx, c, j, "checkin")
			require.NoError(t, err)
			require.Contains(t, reply, "连签 7 天")
			require.Contains(t, reply, "连签奖励：")
			if kind == "physical" || kind == "other" {
				require.Contains(t, reply, "待管理员发放：联系管理员领取奖品")
			}
			reply, err = s.reward(ctx, c, j, "checkin")
			require.NoError(t, err)
			require.Contains(t, reply, "今日已签到")
			records, err := s.ListRewardCodes(ctx, "", "", 1)
			require.NoError(t, err)
			require.Len(t, records.Items, 1)
			require.Equal(t, "streak", records.Items[0].SourceKind)
			var balance float64
			require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
			if kind == "balance" {
				require.Equal(t, 2.5, balance)
			} else {
				require.Equal(t, .5, balance)
				require.Equal(t, "pending", records.Items[0].Status)
			}
			_, err = db.Exec(`INSERT INTO qq_bot_lottery_chance_grants(source_key,user_id,chances) VALUES($1,$2,2)`, fmt.Sprintf("qq-growth:%d:legacy", uid), uid)
			require.NoError(t, err)
			require.NoError(t, s.SaveCheckinConfig(ctx, CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{{ID: "legacy", Name: "Legacy", Kind: "binding", Target: 1, Enabled: true, Reward: &reward}}}}))
			reply, err = s.growthTasks(ctx, c, j)
			require.NoError(t, err)
			require.Contains(t, reply, "已发放")
		})
	}
}

func TestGrowthBalanceFailureRollsBackEntitlement(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	c, err := s.Config(ctx)
	require.NoError(t, err)
	require.NoError(t, s.SaveCheckinConfig(ctx, CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{{ID: "balance", Name: "Balance", Kind: "binding", Target: 1, Enabled: true, Reward: &GrowthReward{Type: "balance", Amount: 2}}}}}))
	_, err = db.Exec(`ALTER TABLE redeem_codes ADD CONSTRAINT reject_growth CHECK(value<>2)`)
	require.NoError(t, err)
	j := Job{GroupID: "group", MemberID: "member"}
	_, err = s.growthTasks(ctx, c, j)
	require.Error(t, err)
	var count int
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, balance)
	_, err = db.Exec(`ALTER TABLE redeem_codes DROP CONSTRAINT reject_growth`)
	require.NoError(t, err)
	reply, err := s.growthTasks(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, reply, "已自动发放")
}
