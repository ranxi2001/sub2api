//go:build integration

package qqbot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLotteryGuaranteeUsesLifetimeCountAndKeepsHistoricalResults(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, cfg := lotteryFixture(t, 60)
	for i := 1; i <= 2; i++ {
		reply, err := s.drawLottery(ctx, c, Job{ID: fmt.Sprint(i), GroupID: "group", MemberID: "member"})
		require.NoError(t, err)
		require.NotContains(t, reply, "保底")
	}
	cfg.GuaranteeEnabled, cfg.GuaranteeEvery = true, 3
	cfg.GuaranteePrizes = []LotteryPrize{{Name: "guaranteed", Amount: 5, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	saved, err := s.LotteryConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, cfg, saved)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(1), state.DrawsUntilGuarantee)
	require.Equal(t, int64(3), state.GuaranteeEvery)
	reply, err := s.lotteryInfo(ctx, c, Job{MemberID: "member"}, true)
	require.NoError(t, err)
	require.Contains(t, reply, "每抽 3 次")
	require.Contains(t, reply, "guaranteed\nUSD 5.00（概率 100.00%）")
	reply, err = s.lotteryInfo(ctx, c, Job{MemberID: "member"}, false)
	require.NoError(t, err)
	require.Contains(t, reply, "距保底 1 次")

	// Other website accounts have their own progress, even in the same group.
	other := user(t, db)
	bind(t, db, other, "other-member")
	_, err = db.Exec(`INSERT INTO payment_orders(user_id,amount) VALUES($1,10)`, other)
	require.NoError(t, err)
	reply, err = s.drawLottery(ctx, c, Job{ID: "other", GroupID: "group", MemberID: "other-member"})
	require.NoError(t, err)
	require.Contains(t, reply, "奖品：win")
	require.NotContains(t, reply, "触发保底")

	third := Job{ID: "3", GroupID: "group", MemberID: "member"}
	original, err := s.drawLottery(ctx, c, third)
	require.NoError(t, err)
	require.Contains(t, original, "本次触发保底抽奖")
	require.Contains(t, original, "奖励额度：USD 5.00")
	require.Contains(t, original, "剩余抽奖次数：3 次")
	require.Contains(t, original, "距保底 3 次")

	cfg.GuaranteeEnabled = false
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	reply, err = s.drawLottery(ctx, c, Job{ID: "4", GroupID: "group", MemberID: "member"})
	require.NoError(t, err)
	require.NotContains(t, reply, "保底")
	cfg.GuaranteeEnabled = true
	// Changing the interval uses lifetime draws; previously paid results remain immutable.
	cfg.GuaranteeEvery = 2
	cfg.GuaranteePrizes[0].Amount = 7
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	replay, err := s.drawLottery(ctx, c, third)
	require.NoError(t, err)
	require.Equal(t, original, replay)
	reply, err = s.drawLottery(ctx, c, Job{ID: "5", GroupID: "group", MemberID: "member"})
	require.NoError(t, err)
	require.NotContains(t, reply, "触发保底")
	require.Contains(t, reply, "距保底 1 次")

	var req UnbindRequest
	require.NoError(t, db.QueryRow(`SELECT app_id,member_id,created_at FROM qq_bot_bindings WHERE user_id=$1`, uid).Scan(&req.AppID, &req.MemberID, &req.CreatedAt))
	require.NoError(t, s.Unbind(ctx, uid, req))
	bind(t, db, uid, "rebound")
	reply, err = s.drawLottery(ctx, c, Job{ID: "6", GroupID: "group", MemberID: "rebound"})
	require.NoError(t, err)
	require.Contains(t, reply, "本次触发保底抽奖")
	require.Contains(t, reply, "奖励额度：USD 7.00")
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(6), state.Used)
	require.Zero(t, state.Remaining)
	require.Equal(t, int64(2), state.DrawsUntilGuarantee)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, float64(16), balance)

	draws, err := s.ListLotteryDraws(ctx, fmt.Sprint(uid), 1, 20)
	require.NoError(t, err)
	require.Len(t, draws.Items, 6)
	for i, draw := range draws.Items {
		require.Equal(t, i == 0 || i == 3, draw.IsGuaranteed)
	}
	records, err := s.ListActivityRecords(ctx, "lottery", fmt.Sprint(uid), 1, 20)
	require.NoError(t, err)
	require.Len(t, records.Items, 6)
	for i, record := range records.Items {
		require.Equal(t, draws.Items[i].IsGuaranteed, record.IsGuaranteed)
	}
	var interval, number int64
	require.NoError(t, db.QueryRow(`SELECT (config_snapshot->>'guarantee_every')::bigint,(config_snapshot->>'draw_number')::bigint FROM qq_bot_lottery_draws WHERE request_key='app:3'`).Scan(&interval, &number))
	require.Equal(t, int64(3), interval)
	require.Equal(t, int64(3), number)
}

func TestLotteryGuaranteeConcurrentDrawsAndMessageReplay(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, cfg := lotteryFixture(t, 60)
	cfg.GuaranteeEnabled, cfg.GuaranteeEvery = true, 3
	cfg.GuaranteePrizes = []LotteryPrize{{Name: "guaranteed", Amount: 5, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	s2 := New(db, testEncryptor{}, true, nil)
	var wg sync.WaitGroup
	errors := make(chan error, 24)
	for i := range 24 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bot := s
			if i%2 == 1 {
				bot = s2
			}
			_, err := bot.drawLottery(ctx, c, Job{ID: fmt.Sprint(i), MessageID: fmt.Sprint(i / 2), GroupID: "group", MemberID: "member"})
			errors <- err
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var total, guaranteed, rewards, ledger int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*),COUNT(*) FILTER(WHERE config_snapshot->'is_guaranteed'='true'::jsonb) FROM qq_bot_lottery_draws WHERE user_id=$1`, uid).Scan(&total, &guaranteed))
	require.Equal(t, 6, total)
	require.Equal(t, 2, guaranteed)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE user_id=$1`, uid).Scan(&rewards))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM redeem_codes WHERE used_by=$1`, uid).Scan(&ledger))
	require.Equal(t, 6, rewards)
	require.Equal(t, 6, ledger)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, float64(14), balance)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(6), state.Earned)
	require.Zero(t, state.Remaining)
	var key, result string
	require.NoError(t, db.QueryRow(`SELECT request_key,result_text FROM qq_bot_lottery_draws WHERE config_snapshot->'is_guaranteed'='true'::jsonb ORDER BY id LIMIT 1`).Scan(&key, &result))
	reply, err := s.drawLottery(ctx, c, Job{ID: "replay", MessageID: strings.TrimPrefix(key, "app:message:"), GroupID: "group", MemberID: "member"})
	require.NoError(t, err)
	require.Equal(t, result, reply)
}

func TestLotteryGuaranteeRollbackDoesNotConsumeProgress(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, cfg := lotteryFixture(t, 10)
	cfg.GuaranteeEnabled, cfg.GuaranteeEvery = true, 1
	cfg.GuaranteePrizes = []LotteryPrize{{Name: "guaranteed", Amount: 2.12345678, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	_, err := db.Exec(`CREATE FUNCTION reject_guarantee_ledger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'ledger unavailable'; END $$; CREATE TRIGGER reject_ledger BEFORE INSERT ON redeem_codes FOR EACH ROW EXECUTE FUNCTION reject_guarantee_ledger();`)
	require.NoError(t, err)
	j := Job{ID: "rollback", GroupID: "group", MemberID: "member"}
	_, err = s.drawLottery(ctx, c, j)
	require.Error(t, err)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Zero(t, state.Used)
	require.Equal(t, int64(1), state.Remaining)
	require.Equal(t, int64(1), state.DrawsUntilGuarantee)
	var balance float64
	var rewards int
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, balance)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE user_id=$1`, uid).Scan(&rewards))
	require.Zero(t, rewards)
	_, err = db.Exec(`DROP TRIGGER reject_ledger ON redeem_codes`)
	require.NoError(t, err)
	reply, err := s.drawLottery(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, reply, "本次触发保底抽奖")
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 2.12345678, balance)
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(1), state.Used)
	require.Zero(t, state.Remaining)
}
