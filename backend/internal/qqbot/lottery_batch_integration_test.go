//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func lotteryBatchTotals(t *testing.T, db *sql.DB, uid int64, wantDraws int, wantBalance float64) {
	t.Helper()
	var draws, rewards, ledger int
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_lottery_draws WHERE user_id=$1`, uid).Scan(&draws))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE user_id=$1 AND kind='lottery'`, uid).Scan(&rewards))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM redeem_codes WHERE used_by=$1 AND type='admin_balance'`, uid).Scan(&ledger))
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, wantDraws, draws)
	require.Equal(t, wantDraws, rewards)
	require.Equal(t, wantDraws, ledger)
	require.Equal(t, wantBalance, balance)
}

func TestLotteryBatchAdvancesLifetimeGuaranteeAndReplaysImmutableResult(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, cfg := lotteryFixture(t, 80)
	cfg.GuaranteeEnabled, cfg.GuaranteeEvery = true, 3
	cfg.GuaranteePrizes = []LotteryPrize{{Name: "guaranteed", Amount: 5, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	_, err := s.drawLottery(ctx, c, Job{ID: "single-first", GroupID: "group", MemberID: "member"})
	require.NoError(t, err)

	j := Job{ID: "batch", MessageID: "batch-message", GroupID: "group", MemberID: "member"}
	result, err := s.drawLotteryCount(ctx, c, j, 5)
	require.NoError(t, err)
	require.Contains(t, result, "win")
	require.Contains(t, result, "guaranteed")
	require.Contains(t, result, "保底")
	require.Contains(t, result, "剩余抽奖次数：2 次")
	require.Contains(t, result, "距保底 3 次")
	lotteryBatchTotals(t, db, uid, 6, 14)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(6), state.Used)
	require.Equal(t, int64(2), state.Remaining)
	require.Equal(t, int64(3), state.DrawsUntilGuarantee)

	rows, err := db.Query(`SELECT (config_snapshot->>'draw_number')::bigint,COALESCE(config_snapshot->'is_guaranteed'='true'::jsonb,false),remaining FROM qq_bot_lottery_draws WHERE user_id=$1 ORDER BY id`, uid)
	require.NoError(t, err)
	var numbers []int64
	var guaranteed []bool
	var remaining []int64
	for rows.Next() {
		var number, left int64
		var isGuaranteed bool
		require.NoError(t, rows.Scan(&number, &isGuaranteed, &left))
		numbers = append(numbers, number)
		guaranteed = append(guaranteed, isGuaranteed)
		remaining = append(remaining, left)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, []int64{1, 2, 3, 4, 5, 6}, numbers)
	require.Equal(t, []bool{false, false, true, false, false, true}, guaranteed)
	require.Equal(t, []int64{7, 6, 5, 4, 3, 2}, remaining)

	// A retry returns the settled batch even if rules or its requested count change.
	cfg.GuaranteeEnabled = false
	cfg.Prizes[0].Amount = 2
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	replay, err := s.drawLotteryCount(ctx, c, j, 5)
	require.NoError(t, err)
	require.Equal(t, result, replay)
	j.ID = "new-delivery-job"
	replay, err = s.drawLotteryCount(ctx, c, j, 1)
	require.NoError(t, err)
	require.Equal(t, result, replay)
	lotteryBatchTotals(t, db, uid, 6, 14)
}

func TestLotteryBatchInsufficientChancesDoNotPartiallyDraw(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, _ := lotteryFixture(t, 20)
	result, err := s.drawLotteryCount(ctx, c, Job{ID: "too-many", GroupID: "group", MemberID: "member"}, 3)
	require.NoError(t, err)
	require.Contains(t, result, "抽奖次数不足")
	lotteryBatchTotals(t, db, uid, 0, 0)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(2), state.Remaining)

	j := Job{ID: "all-available", GroupID: "group", MemberID: "member"}
	result, err = s.drawLotteryCount(ctx, c, j, 2)
	require.NoError(t, err)
	lotteryBatchTotals(t, db, uid, 2, 2)
	// Jobs without a QQ message ID also keep a durable, replayable batch result.
	replay, err := s.drawLotteryCount(ctx, c, j, 2)
	require.NoError(t, err)
	require.Equal(t, result, replay)
	lotteryBatchTotals(t, db, uid, 2, 2)
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Zero(t, state.Remaining)
}

func TestLotteryBatchZeroPrizesConsumeChancesAndPreserveFractionalRewards(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, cfg := lotteryFixture(t, 50)
	cfg.Prizes = []LotteryPrize{{Name: "谢谢参与", Amount: 0, ProbabilityBPS: 10000}}
	cfg.GuaranteeEnabled, cfg.GuaranteeEvery = true, 2
	cfg.GuaranteePrizes = []LotteryPrize{{Name: "保底奖励", Amount: .12345678, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	result, err := s.drawLotteryCount(ctx, c, Job{ID: "zero-and-fractional-batch", GroupID: "group", MemberID: "member"}, 5)
	require.NoError(t, err)
	require.Contains(t, result, "谢谢参与 × 3")
	require.Contains(t, result, "保底奖励 × 2")
	require.Contains(t, result, "合计奖励额度：USD 0.25")
	require.Contains(t, result, "剩余抽奖次数：0 次")
	require.Contains(t, result, "本次触发保底抽奖 2 次")
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(5), state.Used)
	require.Zero(t, state.Remaining)
	require.Equal(t, int64(1), state.DrawsUntilGuarantee)
	var draws, zeroDraws, rewards, ledger int
	var balance, rewardTotal, ledgerTotal float64
	require.NoError(t, db.QueryRow(`SELECT COUNT(*),COUNT(*) FILTER(WHERE amount=0) FROM qq_bot_lottery_draws WHERE user_id=$1`, uid).Scan(&draws, &zeroDraws))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*),SUM(amount) FROM qq_bot_rewards WHERE user_id=$1 AND kind='lottery'`, uid).Scan(&rewards, &rewardTotal))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*),SUM(value) FROM redeem_codes WHERE used_by=$1 AND type='admin_balance'`, uid).Scan(&ledger, &ledgerTotal))
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 5, draws)
	require.Equal(t, 3, zeroDraws)
	require.Equal(t, 5, rewards)
	require.Equal(t, 2, ledger)
	require.Equal(t, .24691356, balance)
	require.Equal(t, balance, rewardTotal)
	require.Equal(t, balance, ledgerTotal)
}

func TestLotteryBatchFailureRollsBackEarlierDrawsAndGuaranteeProgress(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, cfg := lotteryFixture(t, 50)
	cfg.GuaranteeEnabled, cfg.GuaranteeEvery = true, 3
	cfg.GuaranteePrizes = []LotteryPrize{{Name: "guaranteed", Amount: 5, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	// Fail after two prizes have already been settled inside the transaction.
	_, err := db.Exec(`CREATE FUNCTION reject_third_batch_ledger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (SELECT COUNT(*) FROM redeem_codes WHERE used_by=NEW.used_by) >= 2 THEN RAISE EXCEPTION 'ledger unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_batch_ledger BEFORE INSERT ON redeem_codes FOR EACH ROW EXECUTE FUNCTION reject_third_batch_ledger();`)
	require.NoError(t, err)
	j := Job{ID: "rollback-batch", MessageID: "rollback-message", GroupID: "group", MemberID: "member"}
	_, err = s.drawLotteryCount(ctx, c, j, 5)
	require.Error(t, err)
	lotteryBatchTotals(t, db, uid, 0, 0)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Zero(t, state.Used)
	require.Equal(t, int64(5), state.Remaining)
	require.Equal(t, int64(3), state.DrawsUntilGuarantee)

	_, err = db.Exec(`DROP TRIGGER reject_batch_ledger ON redeem_codes`)
	require.NoError(t, err)
	_, err = s.drawLotteryCount(ctx, c, j, 5)
	require.NoError(t, err)
	lotteryBatchTotals(t, db, uid, 5, 9)
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(5), state.Used)
	require.Zero(t, state.Remaining)
	require.Equal(t, int64(1), state.DrawsUntilGuarantee)
}

func TestLotteryBatchConcurrentRequestsAndDuplicateMessagesCannotOverspend(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, _ := lotteryFixture(t, 70)
	s2 := New(db, testEncryptor{}, true, nil)
	type outcome struct {
		text string
		err  error
	}
	outcomes := make(chan outcome, 16)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bot := s
			if i%2 == 1 {
				bot = s2
			}
			text, err := bot.drawLotteryCount(ctx, c, Job{ID: fmt.Sprintf("batch-%d", i), MessageID: fmt.Sprintf("message-%d", i/2), GroupID: "group", MemberID: "member"}, 3)
			outcomes <- outcome{text, err}
		}(i)
	}
	wg.Wait()
	close(outcomes)
	won := 0
	for outcome := range outcomes {
		require.NoError(t, outcome.err)
		if !strings.Contains(outcome.text, "抽奖次数不足") {
			won++
		}
	}
	// Two distinct batches win, and each message's duplicate replays its result.
	require.Equal(t, 4, won)
	lotteryBatchTotals(t, db, uid, 6, 6)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(6), state.Used)
	require.Equal(t, int64(1), state.Remaining)
}

func TestLotteryBatchCommandRoutesCountAndKeepsDefaultSingleDraw(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, eventType string
	}{
		{"unmentioned plain", "", groupMessageCreate},
		{"unmentioned slash", "/", groupMessageCreate},
		{"mentioned plain", "<@bot> ", groupAtMessageCreate},
		{"mentioned slash", "<@bot> /", groupAtMessageCreate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, db, c, uid, _ := lotteryFixture(t, 200)
			var replies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
				case "/v2/groups/group/messages":
					replies = append(replies, readQQTestReply(t, r).text())
					_, _ = w.Write([]byte(`{"id":"sent"}`))
				default:
					t.Errorf("lottery command called unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			enqueue := func(id, content string) Payload {
				e := Event{ID: id, GroupID: "group", Content: tc.prefix + content}
				e.Author.MemberID = "member"
				raw, err := json.Marshal(e)
				require.NoError(t, err)
				p := Payload{ID: id, Type: tc.eventType, Data: raw}
				require.NoError(t, s.Enqueue(ctx, c, p))
				return p
			}
			batch := enqueue("batch-command", "抽奖 5")
			s.runOne(ctx)
			require.Len(t, replies, 1)
			require.Contains(t, replies[0], "剩余抽奖次数：15 次")
			var kind, persisted string
			require.NoError(t, db.QueryRow(`SELECT kind,result_text FROM qq_bot_jobs WHERE message_id='batch-command'`).Scan(&kind, &persisted))
			require.Equal(t, "lottery", kind)
			require.NotEmpty(t, persisted)
			require.Contains(t, replies[0], persisted)
			lotteryBatchTotals(t, db, uid, 5, 5)

			for i, command := range []string{"抽奖 0", "抽奖 11", "抽奖 错误", "抽奖 2 3"} {
				enqueue(fmt.Sprintf("invalid-%d", i), command)
				s.runOne(ctx)
				require.Len(t, replies, i+2)
				require.Contains(t, replies[len(replies)-1], "用法")
				lotteryBatchTotals(t, db, uid, 5, 5)
			}
			enqueue("default-command", "抽奖")
			s.runOne(ctx)
			lotteryBatchTotals(t, db, uid, 6, 6)
			enqueue("max-command", "抽奖 10")
			s.runOne(ctx)
			require.Len(t, replies, 7)
			require.Contains(t, replies[len(replies)-1], "剩余抽奖次数：4 次")
			lotteryBatchTotals(t, db, uid, 16, 16)

			// Once its job is pruned, the message still returns the original whole batch.
			_, err := db.Exec(`DELETE FROM qq_bot_jobs WHERE message_id='batch-command'`)
			require.NoError(t, err)
			require.NoError(t, s.Enqueue(ctx, c, batch))
			s.runOne(ctx)
			require.Equal(t, replies[0], replies[len(replies)-1])
			lotteryBatchTotals(t, db, uid, 16, 16)
		})
	}
}
