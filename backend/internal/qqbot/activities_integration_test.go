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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func lotteryFixture(t *testing.T, amount float64) (*Service, *sql.DB, Config, int64, LotteryConfig) {
	t.Helper()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	_, err := db.Exec(`INSERT INTO payment_orders(user_id,amount) VALUES($1,$2)`, uid, amount)
	require.NoError(t, err)
	c, err := s.Config(context.Background())
	require.NoError(t, err)
	lottery := defaultLotteryConfig()
	lottery.Enabled = true
	lottery.Prizes = []LotteryPrize{{Name: "win", Amount: 1, ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(context.Background(), lottery))
	return s, db, c, uid, lottery
}
func TestCheckinRandomRangeLegacyAndZeroDedup(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	cfg, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, normalizeCheckinMilestones(CheckinConfig{RewardDistribution: &CheckinRewardDistribution{7000, 2000, 1000}, Enabled: true, MinAmount: .1, MaxAmount: .1, StreakDays: 7, RechargeRequirement: RechargeRequirement{RechargeWindowDays: 30}}), cfg)
	require.NoError(t, s.SaveCheckinConfig(ctx, CheckinConfig{Enabled: true, MinAmount: .12345678, MaxAmount: .22345678}))
	c, err := s.Config(ctx)
	require.NoError(t, err)
	uid := user(t, db)
	bind(t, db, uid, "member")
	j := Job{MemberID: "member", GroupID: "group"}
	_, err = s.reward(ctx, c, j, "checkin")
	require.NoError(t, err)
	var amount float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&amount))
	require.GreaterOrEqual(t, amount, .12345678)
	require.LessOrEqual(t, amount, .22345678)
	_, err = s.reward(ctx, c, j, "checkin")
	require.NoError(t, err)
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE user_id=$1`, uid).Scan(&n))
	require.Equal(t, 1, n)
	uid2 := user(t, db)
	bind(t, db, uid2, "zero")
	require.NoError(t, s.SaveCheckinConfig(ctx, CheckinConfig{Enabled: true}))
	j.MemberID = "zero"
	text, err := s.reward(ctx, c, j, "checkin")
	require.NoError(t, err)
	require.Contains(t, text, "签到成功")
	text, err = s.reward(ctx, c, j, "checkin")
	require.NoError(t, err)
	require.Contains(t, text, "今日已签到")
	require.NoError(t, s.SaveCheckinConfig(ctx, CheckinConfig{}))
	text, err = s.reward(ctx, c, j, "checkin")
	require.NoError(t, err)
	require.Contains(t, text, "未开启")
}
func TestLotteryConcurrentSpendingReplayAndRebinding(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, _ := lotteryFixture(t, 35)
	var wg sync.WaitGroup
	var won, failed atomic.Int32
	// Two service instances simulate independent workers/processes.
	s2 := New(db, testEncryptor{}, true, nil)
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bot := s
			if i%2 == 0 {
				bot = s2
			}
			text, err := bot.drawLottery(ctx, c, Job{ID: fmt.Sprint(i), GroupID: "group", MemberID: "member"})
			if err != nil {
				failed.Add(1)
			}
			if strings.Contains(text, "奖励额度：USD 1.") {
				won.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.Zero(t, failed.Load())
	require.Equal(t, int32(3), won.Load())
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(3), state.Used)
	require.Zero(t, state.Remaining)
	require.Equal(t, float64(5), state.NextRecharge)
	var key, result string
	require.NoError(t, db.QueryRow(`SELECT request_key,result_text FROM qq_bot_lottery_draws ORDER BY id LIMIT 1`).Scan(&key, &result))
	replay, err := s.drawLottery(ctx, c, Job{ID: strings.TrimPrefix(key, "app:"), GroupID: "group", MemberID: "member"})
	require.NoError(t, err)
	require.Equal(t, result, replay)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, float64(3), balance)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM redeem_codes WHERE used_by=$1`, uid).Scan(&count))
	require.Equal(t, 3, count)
	var req UnbindRequest
	require.NoError(t, db.QueryRow(`SELECT app_id,member_id,created_at FROM qq_bot_bindings WHERE user_id=$1`, uid).Scan(&req.AppID, &req.MemberID, &req.CreatedAt))
	require.NoError(t, s.Unbind(ctx, uid, req))
	bind(t, db, uid, "other")
	text, err := s.drawLottery(ctx, c, Job{ID: "after-rebind", GroupID: "group", MemberID: "other"})
	require.NoError(t, err)
	require.Contains(t, text, "抽奖次数不足")
	page, err := s.ListLotteryDraws(ctx, fmt.Sprint(uid), 1, 2)
	require.NoError(t, err)
	require.Equal(t, 3, page.Total)
	require.Len(t, page.Items, 2)
}
func TestLotteryHistoricalPrincipalAndRefunds(t *testing.T) {
	ctx := context.Background()
	s, db, _, uid, c := lotteryFixture(t, 0)
	_, err := db.Exec(`INSERT INTO payment_orders(user_id,amount,bonus_amount,status,refund_amount,order_type) VALUES
        ($1,25,5,'COMPLETED',0,'balance'),
        ($1,20,4,'PARTIALLY_REFUNDED',10,'balance'),
        ($1,10,0,'REFUND_FAILED',0,'balance'),
        ($1,99,0,'REFUNDED',99,'balance'),
        ($1,99,0,'REFUND_REQUESTED',99,'balance'),
        ($1,99,0,'REFUNDING',99,'balance'),
        ($1,99,0,'REFUND_PENDING',99,'balance'),
        ($1,99,0,'PENDING',0,'balance'),
        ($1,99,0,'FAILED',0,'balance'),
        ($1,99,0,'COMPLETED',0,'subscription')`, uid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO payment_orders(user_id,amount,paid_at,completed_at) VALUES($1,99,NULL,NOW()),($1,99,NOW(),NULL)`, uid)
	require.NoError(t, err)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, float64(38), state.RechargeTotal)
	require.Equal(t, int64(3), state.Remaining)
	require.Equal(t, float64(2), state.NextRecharge)
	// Exact decimal accumulation cannot drop an entitlement at 0.1 + 0.2 = 0.3.
	uid2 := user(t, db)
	_, err = db.Exec(`INSERT INTO payment_orders(user_id,amount) VALUES($1,.1),($1,.2)`, uid2)
	require.NoError(t, err)
	c.RechargeStep = .3
	require.NoError(t, s.SaveLotteryConfig(ctx, c))
	state, err = s.LotteryState(ctx, uid2)
	require.NoError(t, err)
	require.Equal(t, int64(1), state.Remaining)
}
func TestLotteryRefundDeficitAndRuleChangesNeverResetUsage(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, lottery := lotteryFixture(t, 20)
	for i := range 2 {
		_, err := s.drawLottery(ctx, c, Job{ID: fmt.Sprint(i), GroupID: "group", MemberID: "member"})
		require.NoError(t, err)
	}
	_, err := db.Exec(`UPDATE payment_orders SET status='PARTIALLY_REFUNDED',refund_amount=15 WHERE user_id=$1`, uid)
	require.NoError(t, err)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(2), state.Used)
	require.Zero(t, state.Remaining)
	require.Equal(t, float64(25), state.NextRecharge)
	lottery.Enabled = false
	require.NoError(t, s.SaveLotteryConfig(ctx, lottery))
	lottery.Enabled = true
	require.NoError(t, s.SaveLotteryConfig(ctx, lottery))
	text, err := s.drawLottery(ctx, c, Job{ID: "disabled-reset", GroupID: "group", MemberID: "member"})
	require.NoError(t, err)
	require.Contains(t, text, "抽奖次数不足")
	_, err = db.Exec(`INSERT INTO payment_orders(user_id,amount) VALUES($1,25)`, uid)
	require.NoError(t, err)
	text, err = s.drawLottery(ctx, c, Job{ID: "covered", GroupID: "group", MemberID: "member"})
	require.NoError(t, err)
	require.Contains(t, text, "奖励额度：USD 1.")
	lottery.RechargeStep = 5
	require.NoError(t, s.SaveLotteryConfig(ctx, lottery))
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(6), state.Earned)
	require.Equal(t, int64(3), state.Used)
	require.Equal(t, int64(3), state.Remaining)
}
func TestLotteryAtomicRollbackAndZeroPrize(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, lottery := lotteryFixture(t, 10)
	_, err := db.Exec(`CREATE FUNCTION reject_lottery_ledger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'ledger unavailable'; END $$; CREATE TRIGGER reject_ledger BEFORE INSERT ON redeem_codes FOR EACH ROW EXECUTE FUNCTION reject_lottery_ledger();`)
	require.NoError(t, err)
	j := Job{ID: "rollback", GroupID: "group", MemberID: "member"}
	_, err = s.drawLottery(ctx, c, j)
	require.Error(t, err)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Zero(t, state.Used)
	require.Equal(t, int64(1), state.Remaining)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, balance)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards`).Scan(&count))
	require.Zero(t, count)
	lottery.Prizes = []LotteryPrize{{Name: "thanks", ProbabilityBPS: 10000}}
	require.NoError(t, s.SaveLotteryConfig(ctx, lottery))
	text, err := s.drawLottery(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, text, "奖励额度：USD 0.00")
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(1), state.Used)
	require.Zero(t, state.Remaining)
	replay, err := s.drawLottery(ctx, c, j)
	require.NoError(t, err)
	require.Equal(t, text, replay)
}
func TestLotteryRequiresBindingAndActiveGroup(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, lottery := lotteryFixture(t, 10)
	for _, j := range []Job{{ID: "unbound", GroupID: "group", MemberID: "stranger"}, {ID: "group", GroupID: "forbidden", MemberID: "member"}} {
		text, err := s.drawLottery(ctx, c, j)
		require.NoError(t, err)
		require.NotContains(t, text, "奖励额度：USD 1.")
	}
	lottery.Enabled = false
	require.NoError(t, s.SaveLotteryConfig(ctx, lottery))
	text, err := s.drawLottery(ctx, c, Job{ID: "disabled", GroupID: "group", MemberID: "member"})
	require.NoError(t, err)
	require.Contains(t, text, "未开启")
	lottery.Enabled = true
	require.NoError(t, s.SaveLotteryConfig(ctx, lottery))
	_, err = db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, uid)
	require.NoError(t, err)
	text, err = s.drawLottery(ctx, c, Job{ID: "inactive", GroupID: "group", MemberID: "member"})
	require.NoError(t, err)
	require.Contains(t, text, "绑定")
}

func TestLotteryWaitsForRefundBeforeEligibility(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, _ := lotteryFixture(t, 10)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE payment_orders SET status='REFUNDED',refund_amount=amount WHERE user_id=$1`, uid)
	require.NoError(t, err)
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := s.drawLottery(ctx, c, Job{ID: "refund-race", GroupID: "group", MemberID: "member"})
		done <- result{text, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("draw ignored locked refund: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.Contains(t, r.text, "抽奖次数不足")
	case <-time.After(5 * time.Second):
		t.Fatal("draw did not resume")
	}
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Zero(t, state.Used)
}
func TestQQWorkerLotteryCommandsAndMessageDedup(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, _ := lotteryFixture(t, 20)
	insightsUsageTable(t, db)
	var replies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
		case "/v2/groups/group/messages":
			body := readQQTestReply(t, r)
			replies = append(replies, body.text())
			_, _ = w.Write([]byte(`{"id":"sent"}`))
		default:
			t.Errorf("activity command called unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	s.qqURL = server.URL
	s.tokenURL = server.URL + "/token"
	enqueue := func(eventType, id, content string) Payload {
		e := Event{ID: id, GroupID: "group", Content: content}
		e.Author.MemberID = "member"
		raw, err := json.Marshal(e)
		require.NoError(t, err)
		p := Payload{ID: id, Type: eventType, Data: raw}
		require.NoError(t, s.Enqueue(ctx, c, p))
		return p
	}
	p := enqueue(groupMessageCreate, "draw", "/抽奖")
	s.runOne(ctx)
	require.NotEmpty(t, replies)
	require.Contains(t, replies[0], "奖励额度：USD 1.")
	require.NoError(t, s.Enqueue(ctx, c, p))
	duplicate := p
	duplicate.Type, duplicate.ID = groupAtMessageCreate, "draw-mention-delivery"
	require.NoError(t, s.Enqueue(ctx, c, duplicate))
	s.runOne(ctx)
	var kind string
	require.NoError(t, db.QueryRow(`SELECT kind FROM qq_bot_jobs WHERE message_id='draw'`).Scan(&kind))
	require.Equal(t, "lottery", kind)
	enqueue(groupMessageCreate, "chances", "/抽奖次数")
	s.runOne(ctx)
	chancesReply := replies[len(replies)-1]
	require.Contains(t, chancesReply, "剩余抽奖次数：1 次")
	require.Contains(t, chancesReply, "再充值 USD 10.00 获赠")
	require.NotContains(t, chancesReply, "概率")
	enqueue(groupAtMessageCreate, "rules", "/抽奖活动")
	s.runOne(ctx)
	rulesReply := replies[len(replies)-1]
	require.Contains(t, rulesReply, "累计有效充值每满 USD 10.00 送 1 次")
	require.Contains(t, rulesReply, "概率 100.00%")
	require.NotContains(t, rulesReply, "剩余抽奖次数")
	// Durable message deduplication also survives pruning the job tombstone.
	_, err := db.Exec(`DELETE FROM qq_bot_jobs WHERE message_id='draw'`)
	require.NoError(t, err)
	require.NoError(t, s.Enqueue(ctx, c, p))
	s.runOne(ctx)
	require.Equal(t, replies[0], replies[len(replies)-1])
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(1), state.Used)
	// @ commands remain supported, with the full event arriving afterward.
	legacy := enqueue(groupAtMessageCreate, "mention-first-draw", "/抽奖")
	s.runOne(ctx)
	legacy.Type = groupMessageCreate
	require.NoError(t, s.Enqueue(ctx, c, legacy))
	s.runOne(ctx)
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, int64(2), state.Used)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, float64(2), balance)
}
