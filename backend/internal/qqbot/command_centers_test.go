package qqbot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCommandPagesShowDirectCommandsAndPrefillButtons(t *testing.T) {
	s, replies := helpTestSender(t)
	c := DefaultConfig()
	j := Job{GroupID: "group", MessageID: "pages", CreatedAt: time.Now()}
	body := strings.Repeat("规则", 400) + "\n\n" + strings.Repeat("规则", 400)
	require.NoError(t, s.sendCommandPage(context.Background(), c, j, "成长任务", body, ""))
	reply := (*replies)[0]
	require.Contains(t, reply.text(), "翻页：成长任务 页码")
	require.NotContains(t, reply.text(), "/成长任务")
	require.NotNil(t, reply.Keyboard)
	require.Equal(t, "成长任务 2", reply.Keyboard.Content.Rows[0].Buttons[0].Action.Data)

	require.NoError(t, s.sendCommandPage(context.Background(), c, j, "成长任务", body, "bad"))
	require.Equal(t, "用法：成长任务；翻页：成长任务 页码。", (*replies)[1].text())
	require.NoError(t, s.sendCommandPage(context.Background(), c, j, "成长任务", body, "3"))
	require.Equal(t, "共 2 页，请发送 成长任务 页码。", (*replies)[2].text())
}

func TestRetiredCommandsNeverExecuteOrCallModels(t *testing.T) {
	s, replies := helpTestSender(t) // No database or model endpoint.
	c := DefaultConfig()
	for _, name := range []string{"help", "我的", "福利", "领取任务", "画图", "奖励兑换", "举报垃圾信息"} {
		t.Run(name, func(t *testing.T) {
			require.False(t, isBuiltinCommand(name))
			require.Error(t, validateCustomCommands([]CustomCommand{{Command: "/" + name, Reply: "must not run", Enabled: true}}))
			c.CustomCommands = []CustomCommand{{Command: "/" + name, Reply: "must not run", Enabled: true}}
			for _, eventType := range []string{groupMessageCreate, groupAtMessageCreate} {
				j := Job{EventType: eventType, GroupID: "group", MessageID: name, CreatedAt: time.Now()}
				before := len(*replies)
				require.NoError(t, s.message(context.Background(), c, &j, Event{Content: "/" + name}))
				if eventType == groupMessageCreate {
					require.Len(t, *replies, before)
				} else {
					require.Len(t, *replies, before+1)
					require.Contains(t, (*replies)[before].text(), "未知指令")
				}
			}
		})
	}
}

func TestSplitCommandsReservedAndInvalidArgumentsNeverSpend(t *testing.T) {
	s, replies := helpTestSender(t)
	for _, cmd := range []string{"签到", "我的余额", "余额", "今日用量", "我的奖励", "抽奖", "抽奖次数", "我的抽奖", "邀请奖励", "邀请活动", "我的邀请", "成长任务", "抽奖活动"} {
		require.True(t, isBuiltinCommand(cmd))
		require.Error(t, validateCustomCommands([]CustomCommand{{Command: "/" + cmd, Reply: "override"}}))
		j := Job{EventType: groupMessageCreate, GroupID: "group", MessageID: cmd, CreatedAt: time.Now()}
		require.NoError(t, s.message(context.Background(), DefaultConfig(), &j, Event{Content: "/" + cmd + " invalid"}))
		require.Contains(t, (*replies)[len(*replies)-1].text(), "用法")
	}
}
