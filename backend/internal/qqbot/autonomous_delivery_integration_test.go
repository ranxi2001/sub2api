//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func alwaysReplyHarness(t *testing.T) *autoHarness {
	h := newAutoHarness(t)
	h.c.Autonomous.ReplyMode = "always"
	h.c.Autonomous.SamplePercent = 0
	h.c.Autonomous.MinMessages = 100
	h.c.Autonomous.CooldownSeconds = 1
	h.c.Autonomous.MemberCooldownSeconds = 0
	h.save(t)
	h.output.Store("这句我接住了。")
	return h
}

func releaseAutonomousPacing(t *testing.T, h *autoHarness) {
	t.Helper()
	_, err := h.db.Exec(`UPDATE qq_bot_jobs SET autonomous_ready_at=NOW()-INTERVAL '1 second'; UPDATE qq_bot_jobs SET reply_reserved_at=NOW()-INTERVAL '5 seconds' WHERE reply_reserved_at IS NOT NULL; UPDATE qq_bot_autonomous_state SET last_reply_at=NOW()-INTERVAL '5 seconds' WHERE last_reply_at IS NOT NULL`)
	require.NoError(t, err)
}

func TestAutonomousAlwaysRepliesAcrossMembersAndShortMessages(t *testing.T) {
	h := alwaysReplyHarness(t)
	for i, text := range []string{"你好", "哈哈", "谢谢", "我也觉得", "嗯", "😂"} {
		h.enqueue(t, fmt.Sprintf("short-%d", i), text, fmt.Sprintf("member-%d", i%2))
	}
	for i := 0; i < 6; i++ {
		releaseAutonomousPacing(t, h)
		h.run()
	}
	require.EqualValues(t, 6, h.calls.Load())
	require.EqualValues(t, 6, h.sends.Load())
	for i := 0; i < 6; i++ {
		j := h.job(t, fmt.Sprintf("short-%d", i))
		require.Equal(t, "completed", j.Status)
		require.Equal(t, "always", j.TriggerReason)
	}
	stats, err := h.s.autonomousStats(context.Background(), h.c)
	require.NoError(t, err)
	for _, reason := range []string{"received", "eligible", "selected", "model_calls", "generated", "sent"} {
		require.Contains(t, stats, AutonomousStat{GroupID: "group", Reason: reason, Count: 6})
	}
	require.NotContains(t, h.request.Load().(string), `\"action\":\"skip\"`)
}

func TestAutonomousBurstCoalescesOnceAndKeepsReplyTarget(t *testing.T) {
	h := alwaysReplyHarness(t)
	h.c.Autonomous.MergeSeconds = 2
	h.save(t)
	p := h.enqueue(t, "first", "我遇到了错误", "alice")
	h.enqueue(t, "second", "刚才重试也不行", "alice")
	last := h.enqueue(t, "third", "你看怎么办", "alice")
	require.NoError(t, h.s.Enqueue(context.Background(), h.c, p))
	p.Type, last.Type = groupAtMessageCreate, groupAtMessageCreate
	require.NoError(t, h.s.Enqueue(context.Background(), h.c, p))
	require.NoError(t, h.s.Enqueue(context.Background(), h.c, last))
	_, err := h.s.claimJob(context.Background())
	require.ErrorIs(t, err, sql.ErrNoRows)
	var n int
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE kind='ambient_chat'`).Scan(&n))
	require.Equal(t, 1, n)
	releaseAutonomousPacing(t, h)
	h.run()
	require.NoError(t, h.s.Enqueue(context.Background(), h.c, last))
	h.run()
	require.EqualValues(t, 1, h.calls.Load())
	require.EqualValues(t, 1, h.sends.Load())
	j := h.job(t, "third")
	require.Equal(t, "completed", j.Status)
	var execution AutonomousExecution
	require.NoError(t, json.Unmarshal(j.AutonomousData, &execution))
	require.Equal(t, 3, execution.BatchMessages)
	request := h.request.Load().(string)
	for _, text := range []string{"我遇到了错误", "刚才重试也不行", "你看怎么办"} {
		require.Equal(t, 1, strings.Count(request, text))
	}
	stats, err := h.s.autonomousStats(context.Background(), h.c)
	require.NoError(t, err)
	require.Contains(t, stats, AutonomousStat{GroupID: "group", Reason: "merged", Count: 2})
}

func TestAutonomousCooldownDefersWithoutSpendingModelCalls(t *testing.T) {
	h := alwaysReplyHarness(t)
	h.enqueue(t, "first", "你好", "alice")
	h.run()
	h.enqueue(t, "second", "接着聊聊", "alice")
	h.run()
	require.EqualValues(t, 1, h.calls.Load())
	j := h.job(t, "second")
	require.Equal(t, "queued", j.Status)
	require.Equal(t, "group_cooldown", j.DecisionReason)
	releaseAutonomousPacing(t, h)
	h.run()
	require.EqualValues(t, 2, h.calls.Load())
	require.Equal(t, "completed", h.job(t, "second").Status)
}

func TestAutonomousQueueIsBoundedAndSingleFlight(t *testing.T) {
	h := alwaysReplyHarness(t)
	for i := 0; i < maxAutonomousQueue+2; i++ {
		h.enqueue(t, fmt.Sprintf("queued-%d", i), fmt.Sprintf("第%d条分享", i), fmt.Sprintf("member-%d", i))
	}
	var count int
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE kind='ambient_chat'`).Scan(&count))
	require.Equal(t, maxAutonomousQueue, count)
	_, err := h.s.claimJob(context.Background())
	require.NoError(t, err)
	_, err = h.s.claimJob(context.Background())
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestAutonomousAlwaysStillHonorsQuotasAndEnding(t *testing.T) {
	h := alwaysReplyHarness(t)
	h.c.Autonomous.DailyCalls = 1
	h.save(t)
	h.enqueue(t, "first", "你好", "alice")
	h.run()
	releaseAutonomousPacing(t, h)
	h.enqueue(t, "second", "还有呢", "bob")
	h.run()
	require.EqualValues(t, 1, h.calls.Load())
	require.Equal(t, "call_limit", h.job(t, "second").DecisionReason)
	h.enqueue(t, "ending", "不用回了", "alice")
	h.run()
	require.EqualValues(t, 1, h.calls.Load())
}

func TestAutonomousDecisionAndCostMetrics(t *testing.T) {
	h := newAutoHarness(t)
	h.output.Store(`{"action":"skip","reason":"already_answered"}`)
	h.enqueue(t, "skip", "这个报错怎么处理", "alice")
	h.run()
	require.Equal(t, "model_skip_already_answered", h.job(t, "skip").DecisionReason)
	stats, err := h.s.autonomousStats(context.Background(), h.c)
	require.NoError(t, err)
	require.Contains(t, stats, AutonomousStat{GroupID: "group", Reason: "skip_tokens", Count: 30})
	require.Contains(t, stats, AutonomousStat{GroupID: "group", Reason: "model_skips", Count: 1})
	h.resetCooldown(t)
	h.output.Store(`{"action":"reply","text":"可以先看看日志。"}`)
	h.hook = func() { h.enqueue(t, "ending", "不用回复", "bob") }
	h.enqueue(t, "discard", "另一个错误怎么处理", "bob")
	h.run()
	require.Equal(t, "conversation_ended", h.job(t, "discard").DecisionReason)
	stats, err = h.s.autonomousStats(context.Background(), h.c)
	require.NoError(t, err)
	require.Contains(t, stats, AutonomousStat{GroupID: "group", Reason: "discarded_tokens", Count: 30})
}

func TestAutonomousAlwaysRespectsReplyAndGroupBudgets(t *testing.T) {
	for _, mode := range []string{"hourly", "daily", "global", "group"} {
		t.Run(mode, func(t *testing.T) {
			h := alwaysReplyHarness(t)
			switch mode {
			case "hourly":
				h.c.Autonomous.HourlyReplies = 1
			case "daily":
				h.c.Autonomous.DailyReplies = 1
			case "global":
				h.c.TotalDailyChatLimit = 1
			case "group":
				limit := 1
				h.c.GroupPolicies = []GroupPolicy{{GroupID: "group", ChatDailyLimit: &limit}}
			}
			h.save(t)
			h.enqueue(t, "first", "你好", "alice")
			h.run()
			releaseAutonomousPacing(t, h)
			h.enqueue(t, "second", "你好", "bob")
			h.run()
			require.EqualValues(t, 1, h.calls.Load())
			require.EqualValues(t, 1, h.sends.Load())
			reasons := map[string]string{"hourly": "reply_limit", "daily": "reply_limit", "global": "global_limit", "group": "group_limit"}
			require.Equal(t, reasons[mode], h.job(t, "second").DecisionReason)
		})
	}
}

func TestAutonomousInputBudgetStopsBeforePaidCall(t *testing.T) {
	h := alwaysReplyHarness(t)
	h.c.SystemPrompt = strings.Repeat("龘", 2400)
	h.c.Autonomous.InputTokenBudget = 2048
	h.save(t)
	h.enqueue(t, "budget", "你好", "alice")
	h.run()
	require.Zero(t, h.calls.Load())
	require.Equal(t, "input_budget", h.job(t, "budget").DecisionReason)
	stats, err := h.s.autonomousStats(context.Background(), h.c)
	require.NoError(t, err)
	require.Contains(t, stats, AutonomousStat{GroupID: "group", Reason: "model_calls", Count: 0})
}

func TestAutonomousDeferredExpiryAndEndingThenNewConversation(t *testing.T) {
	h := alwaysReplyHarness(t)
	h.c.Autonomous.MergeSeconds = 2
	h.save(t)
	h.enqueue(t, "old", "有个问题", "alice")
	h.enqueue(t, "end", "不用回复", "alice")
	h.enqueue(t, "new", "又有个问题", "alice")
	releaseAutonomousPacing(t, h)
	h.run()
	h.run()
	require.Equal(t, "conversation_ended", h.job(t, "old").DecisionReason)
	require.Equal(t, "completed", h.job(t, "new").Status)
	h.enqueue(t, "stale", "排队新问题", "bob")
	_, err := h.db.Exec(`UPDATE qq_bot_jobs SET created_at=NOW()-INTERVAL '3 minutes',autonomous_ready_at=NOW()+INTERVAL '1 day' WHERE message_id='stale'`)
	require.NoError(t, err)
	h.run()
	require.Equal(t, "stale", h.job(t, "stale").DecisionReason)
	require.EqualValues(t, 1, h.calls.Load())
}
