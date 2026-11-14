package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func helpTestSender(t *testing.T) (*Service, *[]qqTestReply) {
	t.Helper()
	var replies []qqTestReply
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
		case "/v2/groups/group/messages":
			replies = append(replies, readQQTestReply(t, r))
			_, _ = w.Write([]byte(`{"id":"sent"}`))
		default:
			t.Errorf("help unexpectedly called %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	s := New(nil, nil, false, nil)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	return s, &replies
}

func TestHelpListsAllCommandsInOneMessage(t *testing.T) {
	for count := 0; count <= maxCustomCommands; count++ {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, replies := helpTestSender(t)
			c := DefaultConfig()
			want := []string{"签到", "我的余额", "今日用量", "我的奖励", "抽奖", "抽奖次数", "抽奖活动", "成长任务", "我的优惠券", "邀请奖励", "我的邀请", "今日排行榜", "渠道状态", "生图 图片描述", "价格 模型名", "绑定 邮箱或绑定码", "转账 金额 邮箱"}
			for i := 0; i < count; i++ {
				name := "/" + strings.Repeat("长", 30) + fmt.Sprintf("%02d", i)
				c.CustomCommands = append(c.CustomCommands, CustomCommand{Command: name, Reply: "reply", Enabled: true})
				want = append(want, strings.TrimPrefix(name, "/"))
			}
			c.CustomCommands = append(c.CustomCommands, CustomCommand{Command: "/disabled"}, CustomCommand{Command: "/我的余额", Enabled: true}, CustomCommand{Command: "/我的", Enabled: true}, CustomCommand{Command: "/举报垃圾信息", Enabled: true})
			j := Job{EventType: groupMessageCreate, GroupID: "group", MemberID: strings.Repeat("m", 128), MessageID: "help", CreatedAt: time.Now()}
			require.NoError(t, s.message(context.Background(), c, &j, Event{Content: "/帮助"}))
			require.Len(t, *replies, 1, "all commands must fit one message without truncation")
			reply := (*replies)[0]
			require.Equal(t, 1, reply.Sequence)
			require.Equal(t, j.MessageID, reply.MsgID)
			require.Empty(t, reply.EventID)
			require.Contains(t, reply.text(), j.MemberID)
			displayed := map[string]int{}
			for _, line := range strings.Split(reply.text(), "\n") {
				line = strings.TrimSpace(line)
				if !strings.Contains(line, "：") {
					continue
				}
				command, _, ok := strings.Cut(line, "：")
				require.True(t, ok, "help entries must include a description: %q", line)
				require.True(t, strings.HasPrefix(command, "- **") && strings.HasSuffix(command, "**"), "help commands must be bold list items: %q", line)
				command = strings.TrimSuffix(strings.TrimPrefix(command, "- **"), "**")
				displayed[command]++
				require.NotContains(t, line, " · ", "help entries must use one command per line")
				require.False(t, strings.HasPrefix(command, "/"), "help displays commands directly")
			}
			for _, cmd := range want {
				usage := cmd
				for _, item := range helpSystemItems() {
					if item.command == cmd && item.usage != "" {
						usage = item.usage
						break
					}
				}
				require.Equal(t, 1, displayed[usage], cmd)
			}
			for _, item := range helpSystemItems() {
				description := item.description
				usage := item.command
				if item.usage != "" {
					usage = item.usage
				}
				require.Contains(t, reply.text(), "- **"+usage+"**："+description)
			}
			for i := 0; i < count; i++ {
				name := "/" + strings.Repeat("长", 30) + fmt.Sprintf("%02d", i)
				require.Contains(t, reply.text(), "- **"+replyPlainLabel(strings.TrimPrefix(name, "/"))+"**：自定义回复")
			}
			for _, absent := range []string{"disabled", "福利", "兑换", "翻页", "下一页", "上一页", "更多服务", "第 1/", "举报垃圾信息"} {
				require.NotContains(t, reply.text(), absent)
			}
			for _, section := range []string{"账户与签到", "活动与奖励", "查询与工具", "账号与转账"} {
				require.Contains(t, reply.text(), "\n\n**"+section+"**\n\n- **")
			}
			require.Equal(t, count > 0, strings.Contains(reply.text(), "**自定义指令**"))
			require.Contains(t, reply.text(), "**抽奖 [次数]**：")
			require.NotContains(t, reply.text(), `\[次数\]`)
			require.Contains(t, reply.text(), helpText(c), "QQ and the admin preview must use the same complete help text")
			require.Contains(t, reply.text(), "聊天请 @机器人 + 问题。", "the final line must not be truncated")
			require.LessOrEqual(t, utf8.RuneCountInString(reply.text()), 1800)
			require.NotNil(t, reply.Keyboard)
			require.Len(t, reply.Keyboard.Content.Rows, 4)
			var buttons []string
			ids := map[string]bool{}
			for _, row := range reply.Keyboard.Content.Rows {
				require.Len(t, row.Buttons, 2)
				for _, button := range row.Buttons {
					require.False(t, ids[button.ID])
					ids[button.ID] = true
					require.Equal(t, button.RenderData.Label, button.RenderData.VisitedLabel)
					require.Equal(t, 2, button.Action.Type)
					require.Equal(t, 2, button.Action.Permission.Type)
					buttons = append(buttons, button.Action.Data)
				}
			}
			require.Equal(t, want[:8], buttons)
		})
	}
}

func TestHelpRejectsArgumentsAndPageCallbacks(t *testing.T) {
	s, replies := helpTestSender(t)
	c := DefaultConfig()
	c.CustomCommands = []CustomCommand{{Command: "/disabled", Enabled: false}, {Command: "/hello", Enabled: true, Reply: "hello"}, {Command: "/a", Enabled: true}, {Command: "/b", Enabled: true}, {Command: "/c", Enabled: true}}
	for _, arg := range []string{"0", "-1", "abc", "9999999999999999999999999", "1", "2"} {
		j := Job{GroupID: "group", MessageID: "invalid", CreatedAt: time.Now()}
		require.NoError(t, s.sendHelp(context.Background(), c, j, arg))
		require.Nil(t, (*replies)[len(*replies)-1].Keyboard)
		require.Equal(t, "用法：帮助", (*replies)[len(*replies)-1].text())
	}
	for _, action := range []string{"", "checkin\n/抽奖", "bind", "redeem", "image", "transfer", "report_spam", "custom:/举报垃圾信息", "custom:/disabled", "custom:/签到", "page:0", "page:01", "page:1", "page:2", "page:4", "prices", "growth_claim", "/转账 1 someone@example.com"} {
		_, ok := helpActionCommand(c, action)
		require.False(t, ok, action)
	}
	cmd, ok := helpActionCommand(c, "custom:/hello")
	require.True(t, ok)
	require.Equal(t, "hello", cmd)
	for action, want := range map[string]string{"checkin": "签到", "balance": "我的余额", "growth": "成长任务", "rankings": "今日排行榜"} {
		cmd, ok := helpActionCommand(c, action)
		require.True(t, ok, "only current actions are allowed")
		require.Equal(t, want, cmd)
	}
}

func TestInteractionReplyUsesEventIDAndClickingMember(t *testing.T) {
	s, replies := helpTestSender(t)
	j := Job{EventType: interactionCreate, GroupID: "group", MemberID: "clicker", MessageID: "outer-event-id", CreatedAt: time.Now()}
	require.NoError(t, s.message(context.Background(), DefaultConfig(), &j, Event{Content: "/帮助"}))
	require.Len(t, *replies, 1)
	for i, reply := range *replies {
		require.Equal(t, "outer-event-id", reply.EventID)
		require.Empty(t, reply.MsgID)
		require.Equal(t, i+1, reply.Sequence)
		require.Contains(t, reply.text(), `<qqbot-at-user id="clicker" />`)
		require.NotNil(t, reply.Keyboard)
		// Prefill buttons never contain an auto-send option or example arguments.
		raw, err := json.Marshal(reply.Keyboard)
		require.NoError(t, err)
		require.NotContains(t, string(raw), `"enter":true`)
		require.NotContains(t, string(raw), "邮箱或绑定码")
	}
}

func TestHelpStopsAfterSendFailure(t *testing.T) {
	var sequences []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
		case "/v2/groups/group/messages":
			reply := readQQTestReply(t, r)
			sequences = append(sequences, reply.Sequence)
			if reply.Sequence == 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(`{"id":"sent"}`))
		default:
			t.Errorf("help unexpectedly called %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	s := New(nil, nil, false, nil)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	c := DefaultConfig()
	for i := 0; i < 4; i++ {
		c.CustomCommands = append(c.CustomCommands, CustomCommand{Command: fmt.Sprintf("/custom%d", i), Enabled: true})
	}
	j := Job{GroupID: "group", MessageID: "help", CreatedAt: time.Now()}
	require.Error(t, s.sendHelp(context.Background(), c, j, ""))
	require.Equal(t, []int{1}, sequences, "a failed menu send must be returned to the caller")
}
