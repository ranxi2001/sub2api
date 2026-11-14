//go:build integration

package qqbot

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLotteryChancePrizeIsAvailableImmediatelyAndReplaysOnce(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, cfg := lotteryFixture(t, 10)
	cfg.Prizes = []LotteryPrize{{Name: "再来一次", Chances: 1, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	j := Job{ID: "chance-prize", MessageID: "chance-prize-message", GroupID: "group", MemberID: "member"}
	result, err := s.drawLottery(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, result, "奖励抽奖次数：1 次")
	require.Contains(t, result, "剩余抽奖次数：1 次")
	// Redelivery uses the immutable draw, even after the configured prize changes.
	cfg.Prizes[0].Chances = 5
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	j.ID = "chance-prize-replay"
	replay, err := s.drawLottery(ctx, c, j)
	require.NoError(t, err)
	require.Equal(t, result, replay)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(2), state.Earned)
	require.Equal(t, int64(1), state.BonusChances)
	require.Equal(t, int64(1), state.Used)
	require.Equal(t, int64(1), state.Remaining)

	draws, err := s.ListLotteryDraws(ctx, fmt.Sprint(uid), 1, 20)
	require.NoError(t, err)
	require.Len(t, draws.Items, 1)
	require.Equal(t, 1, draws.Items[0].Chances)
	records, err := s.ListActivityRecords(ctx, "lottery", fmt.Sprint(uid), 1, 20)
	require.NoError(t, err)
	require.Len(t, records.Items, 1)
	require.Equal(t, 1, records.Items[0].Chances)
	require.Equal(t, "credited", records.Items[0].Status)
	var balance float64
	var ledgerCount int
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM redeem_codes WHERE used_by=$1`, uid).Scan(&ledgerCount))
	require.Zero(t, balance)
	require.Zero(t, ledgerCount)

	// The returned chance can fund a later command without any further recharge.
	_, err = s.drawLottery(ctx, c, Job{ID: "use-returned-chance", MemberID: "member", GroupID: "group"})
	require.NoError(t, err)
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(6), state.BonusChances)
	require.Equal(t, int64(2), state.Used)
	require.Equal(t, int64(5), state.Remaining)
}

func TestLotteryBatchCreditsBalanceAndChancesWithPureChanceGuarantee(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, cfg := lotteryFixture(t, 30)
	cfg.Prizes = []LotteryPrize{{Name: "额度加次数", Amount: .125, Chances: 2, ProbabilityBPS: 10000}}
	cfg.GuaranteeEnabled, cfg.GuaranteeEvery = true, 2
	cfg.GuaranteePrizes = []LotteryPrize{{Name: "保底次数", Chances: 3, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	result, err := s.drawLotteryCount(ctx, c, Job{ID: "mixed-prize-batch", GroupID: "group", MemberID: "member"}, 3)
	require.NoError(t, err)
	require.Contains(t, result, "合计奖励额度：USD 0.25")
	require.Contains(t, result, "合计奖励抽奖次数：7 次")
	require.Contains(t, result, "剩余抽奖次数：7 次")
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(7), state.BonusChances)
	require.Equal(t, int64(3), state.Used)
	require.Equal(t, int64(7), state.Remaining)
	rows, err := db.Query(`SELECT earned,remaining,(config_snapshot->>'awarded_chances')::integer FROM qq_bot_lottery_draws WHERE user_id=$1 ORDER BY id`, uid)
	require.NoError(t, err)
	var earned, remaining, chances []int64
	for rows.Next() {
		var e, r, ch int64
		require.NoError(t, rows.Scan(&e, &r, &ch))
		earned, remaining, chances = append(earned, e), append(remaining, r), append(chances, ch)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, []int64{5, 8, 10}, earned)
	require.Equal(t, []int64{4, 6, 7}, remaining)
	require.Equal(t, []int64{2, 3, 2}, chances)
	var balance, rewards float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.NoError(t, db.QueryRow(`SELECT SUM(amount) FROM qq_bot_rewards WHERE user_id=$1`, uid).Scan(&rewards))
	require.Equal(t, .25, balance)
	require.Equal(t, balance, rewards)
	rules, err := s.lotteryInfo(ctx, c, Job{MemberID: "member"}, true)
	require.NoError(t, err)
	require.Contains(t, rules, "USD 0.12 + 抽奖次数 2 次")
	require.Contains(t, rules, "保底次数\n抽奖次数 3 次")
}

func TestLotteryChancePrizeTransactionRollbackAndBudget(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, cfg := lotteryFixture(t, 30)
	cfg.Prizes = []LotteryPrize{{Name: "额度加次数", Amount: 1, Chances: 2, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	_, err := db.Exec(`CREATE FUNCTION reject_lottery_chance_ledger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'ledger unavailable'; END $$; CREATE TRIGGER reject_lottery_chance_ledger BEFORE INSERT ON redeem_codes FOR EACH ROW EXECUTE FUNCTION reject_lottery_chance_ledger();`)
	require.NoError(t, err)
	j := Job{ID: "rollback-chance-batch", MemberID: "member", GroupID: "group"}
	_, err = s.drawLotteryCount(ctx, c, j, 3)
	require.Error(t, err)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Zero(t, state.Used)
	require.Zero(t, state.BonusChances)
	require.Equal(t, int64(3), state.Remaining)
	var rewards int
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE user_id=$1`, uid).Scan(&rewards))
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, rewards)
	require.Zero(t, balance)
	_, err = db.Exec(`DROP TRIGGER reject_lottery_chance_ledger ON redeem_codes`)
	require.NoError(t, err)
	budget := 2.0
	c.RewardBudgetDaily = &budget
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	result, err := s.drawLotteryCount(ctx, c, j, 3)
	require.NoError(t, err)
	require.Contains(t, result, "今日余额奖励预算已用尽")
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Zero(t, state.Used)
	require.Zero(t, state.BonusChances)
	// A zero-balance "again" prize remains usable when balance budget is zero.
	cfg.Prizes[0].Amount = 0
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	_, err = s.drawLotteryCount(ctx, c, j, 3)
	require.NoError(t, err)
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(6), state.BonusChances)
	require.Equal(t, int64(3), state.Used)
	require.Equal(t, int64(6), state.Remaining)
}

func TestLotteryChancePrizeConcurrentDuplicateMessagesGrantOnce(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, cfg := lotteryFixture(t, 10)
	cfg.Prizes = []LotteryPrize{{Name: "再来一次", Chances: 1, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	s2 := New(db, testEncryptor{}, true, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bot := s
			if i%2 == 1 {
				bot = s2
			}
			_, err := bot.drawLottery(ctx, c, Job{ID: fmt.Sprint("delivery-", i), MessageID: "same-message", MemberID: "member", GroupID: "group"})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(1), state.Used)
	require.Equal(t, int64(1), state.BonusChances)
	require.Equal(t, int64(1), state.Remaining)
}
