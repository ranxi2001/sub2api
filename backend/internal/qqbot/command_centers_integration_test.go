//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSplitCommandsKeepRepliesFocusedAndDeduplicateDraw(t *testing.T) {
	s, db, c, uid, _ := lotteryFixture(t, 20)
	insightsUsageTable(t, db)
	ctx := context.Background()
	require.NoError(t, s.SaveCheckinConfig(ctx, CheckinConfig{Enabled: true, Growth: GrowthConfig{BindingChances: 2}}))
	_, err := db.Exec(`UPDATE users SET balance=12.5 WHERE id=$1`, uid)
	require.NoError(t, err)
	sender, replies := helpTestSender(t)
	s.qqURL, s.tokenURL = sender.qqURL, sender.tokenURL
	enqueue := func(id, content string) {
		e := Event{ID: id, GroupID: "group", Content: content}
		e.Author.MemberID = "member"
		raw, err := json.Marshal(e)
		require.NoError(t, err)
		require.NoError(t, s.Enqueue(ctx, c, Payload{ID: "outer-" + id, Type: groupMessageCreate, Data: raw}))
		s.runOne(ctx)
	}
	for i, tc := range []struct {
		cmd, want string
		absent    []string
	}{
		{"/我的余额", "USD 12.50", []string{"今日用量", "累计奖励", "剩余抽奖"}},
		{"我的余额", "USD 12.50", []string{"今日用量", "累计奖励", "剩余抽奖"}},
		{"/余额", "USD 12.50", []string{"今日用量", "累计奖励", "剩余抽奖"}},
		{"余额", "USD 12.50", []string{"今日用量", "累计奖励", "剩余抽奖"}},
		{"/今日用量", "今日用量", []string{"账户额度", "累计奖励", "抽奖次数"}},
		{"/我的奖励", "奖励", []string{"今日用量", "抽奖规则", "成长任务"}},
		{"/抽奖次数", "剩余", []string{"今日用量", "邀请规则"}},
		{"/我的邀请", "邀请", []string{"今日用量", "成长任务"}},
	} {
		enqueue(fmt.Sprint(i), tc.cmd)
		require.Len(t, *replies, i+1)
		reply := (*replies)[len(*replies)-1].text()
		require.Contains(t, reply, tc.want)
		for _, absent := range tc.absent {
			require.NotContains(t, reply, absent)
		}
	}
	enqueue("growth", "/成长任务")
	require.Contains(t, (*replies)[len(*replies)-1].text(), "已自动发放")
	require.NotContains(t, (*replies)[len(*replies)-1].text(), "/福利")
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.EqualValues(t, 4, state.Remaining)
	enqueue("draw", "/抽奖")
	require.Contains(t, (*replies)[len(*replies)-1].text(), "抽奖结果")
	enqueue("draw", "/抽奖")
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.EqualValues(t, 1, state.Used)
	require.EqualValues(t, 3, state.Remaining)
	before := len(*replies)
	for i, cmd := range []string{"/我的", "/福利", "/福利 抽奖", "/领取任务 1", "/奖励兑换 old-code"} {
		enqueue(fmt.Sprintf("retired-%d", i), cmd)
	}
	require.Len(t, *replies, before)
	// Invalid pages must not grant an eligible task.
	require.NoError(t, s.SaveCheckinConfig(ctx, CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{{ID: "invalid-page", Name: "Not yet", Kind: "binding", Target: 1, Enabled: true, Chances: 10}}}}))
	require.NoError(t, s.message(ctx, c, &Job{GroupID: "group", MemberID: "member", MessageID: "invalid", CreatedAt: time.Now()}, Event{Content: "/成长任务 R1"}))
	require.Contains(t, (*replies)[len(*replies)-1].text(), "用法")
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.EqualValues(t, 3, state.Remaining)
}
