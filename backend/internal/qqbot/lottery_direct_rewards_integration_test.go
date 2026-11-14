//go:build integration

package qqbot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLotteryDirectChancesCombineWithRechargeAndSurviveRuleChanges(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, cfg := lotteryFixture(t, 10)
	_, err := db.Exec(`INSERT INTO qq_bot_lottery_chance_grants(source_key,user_id,chances) VALUES('gpt-direct:test',$1,2)`, uid)
	require.NoError(t, err)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(3), state.Earned)
	require.Equal(t, int64(2), state.BonusChances)
	// Draw from the existing pool; changing balance recharge rules cannot erase grants.
	cfg.RechargeStep = 100
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(2), state.Remaining)
	var wg sync.WaitGroup
	var won, failed atomic.Int32
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := s.drawLottery(ctx, c, Job{ID: fmt.Sprint(i), MemberID: "member", GroupID: "group"})
			if err != nil {
				failed.Add(1)
			}
			if strings.Contains(result, "奖励额度：USD 1.00") {
				won.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.Zero(t, failed.Load())
	require.Equal(t, int32(2), won.Load())
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Zero(t, state.Remaining)
	require.Equal(t, float64(90), state.NextRecharge)
	// Activity winnings remain excluded from recharge-based earning.
	require.Equal(t, float64(10), state.RechargeTotal)
}
