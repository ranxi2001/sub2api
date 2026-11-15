package qqbot

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutonomousPolicyShortMessagesAndOutput(t *testing.T) {
	a := DefaultAutonomousConfig()
	a.ReplyMode = "always"
	for _, text := range []string{"好", "谢谢", "哈哈", "你好", "😂"} {
		got, reason := autonomousPolicyText(a, Event{Content: text})
		require.Empty(t, reason, text)
		require.Equal(t, text, got)
	}
	for _, text := range []string{"不聊了", "不用回了", "再见！"} {
		_, reason := autonomousPolicyText(a, Event{Content: text})
		require.Equal(t, "conversation_ended", reason)
	}
	for _, text := range []string{"/签到", "@张三", "密码123", "https://example.com", "？？"} {
		_, reason := autonomousPolicyText(a, Event{Content: text})
		require.NotEmpty(t, reason)
	}
	text, reason, err := parseAutonomousDecision("这个我也觉得挺有意思。", 100, "always")
	require.NoError(t, err)
	require.NotEmpty(t, text)
	require.Empty(t, reason)
	_, _, err = parseAutonomousDecision(`{"action":"skip","reason":"no_value"}`, 100, "always")
	require.Error(t, err)
	for skip := range autonomousSkipReasons {
		_, reason, err = parseAutonomousDecision(`{"action":"skip","reason":"`+skip+`"}`, 100, "natural")
		require.NoError(t, err)
		require.Equal(t, "model_skip_"+skip, reason)
	}
	_, _, err = parseAutonomousDecision(`{"action":"skip","reason":"arbitrary explanation"}`, 100, "natural")
	require.Error(t, err)
}

func TestAutonomousPolicyPromptHasOneOutputContract(t *testing.T) {
	c := DefaultConfig()
	c.HistoryMessages = 0
	s := &Service{}
	for _, mode := range []string{"quiet", "natural", "active", "always"} {
		c.Autonomous.ReplyMode = mode
		for _, trigger := range []string{"sampled", "followup"} {
			msgs := s.autonomousHistory(context.Background(), c, Job{TriggerReason: trigger}, "你好")
			for _, m := range msgs {
				require.NotContains(t, m.Content, "用自然语言回复，不输出 JSON 包装")
				require.NotContains(t, m.Content, naturalConversationInstructions)
			}
			prompt := autonomousPolicyInstructions(c.Autonomous, trigger == "followup")
			if mode == "always" {
				require.NotContains(t, prompt, `"action":"skip"`)
				require.Contains(t, prompt, "不得选择沉默")
			} else {
				require.Contains(t, prompt, `"action":"skip"`)
			}
		}
	}
}

func TestAutonomousBudgetPreservesMandatoryInput(t *testing.T) {
	history := groupHistoryEntry{Speaker: "member", ID: "merged", Text: strings.Repeat("聊天历史。", 200)}.message("user")
	current := groupHistoryEntry{Speaker: "member", Text: "当前问题"}.message("user")
	msgs := []Message{{Role: "system", Content: "原有人设"}, {Role: "system", Content: "原有人设"}, {Role: "system", Content: siteKnowledgeInstructions + strings.Repeat("知识", 500)}, history, {Role: "system", Content: "输出协议"}, current}
	got, estimate, err := budgetAutonomousInput(msgs, 100, []string{"merged"})
	require.NoError(t, err)
	require.LessOrEqual(t, estimate.Total, 100)
	require.Positive(t, estimate.Removed)
	require.Equal(t, []Message{{Role: "system", Content: "原有人设"}, {Role: "system", Content: "输出协议"}, current}, got)
	_, _, err = budgetAutonomousInput(msgs, 1, nil)
	require.Error(t, err)
	current.Images = []string{"data:image/png;base64,aQ=="}
	estimate = estimateAutonomousInput([]Message{current})
	require.Equal(t, autonomousImageAllowance, estimate.Images)
	require.Equal(t, estimate.Current+estimate.Images, estimate.Total)
	current.Images = []string{"image-1", "image-2", "image-3", "image-4"}
	c := DefaultConfig()
	_, _, err = budgetAutonomousInput([]Message{{Role: "system", Content: c.SystemPrompt}, {Role: "system", Content: sharedGroupHistoryInstructions}, {Role: "system", Content: autonomousPolicyInstructions(c.Autonomous, false)}, current}, c.Autonomous.InputTokenBudget, nil)
	require.NoError(t, err, "default input budget must still accept four QQ images")
}

func TestAutonomousBurstKeepsLatestTargetAndSpeaker(t *testing.T) {
	first := Event{ID: "first", GroupID: "group", Content: "第一句"}
	first.Author.MemberID = "alice"
	last := first
	last.ID, last.Content = "last", "第二句"
	got, ok := mergeAutonomousEvents(first, last)
	require.True(t, ok)
	require.Equal(t, "last", got.ID)
	require.Equal(t, "第一句\n第二句", got.Content)
	last.Author.MemberID = "bob"
	_, ok = mergeAutonomousEvents(first, last)
	require.False(t, ok)
	last = first
	last.Content = strings.Repeat("字", 1501)
	_, ok = mergeAutonomousEvents(first, last)
	require.False(t, ok)
}
