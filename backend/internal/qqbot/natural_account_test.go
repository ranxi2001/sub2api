package qqbot

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestNaturalAccountCommandMatchesCompletePersonalRequests(t *testing.T) {
	for command, requests := range map[string][]string{
		"我的余额": {"帮我看一下我的余额", "<@bot> 请帮我查一下余额？", "麻烦你查看我的账户余额", "我的余额还有多少", "我的钱包里还有多少钱呀", "看看我还有多少钱", "余额是多少呢", "查一下本人余额", "我还剩多少美元"},
		"今日用量": {"看看我今天的用量", "帮我查询今日用量", "我的今日使用情况", "我今天消耗了多少", "今天花费多少？"},
		"抽奖次数": {"我还有多少次抽奖", "帮我看看我的抽奖次数", "请问剩余抽奖次数是多少", "我的抽奖机会还剩多少次"},
	} {
		for _, request := range requests {
			t.Run(request, func(t *testing.T) { require.Equal(t, command, naturalAccountCommand(request)) })
		}
	}
	for _, request := range []string{"", "/我的余额", "帮我看一下他的余额", "帮小王看余额", "余额是什么", "怎么查我的余额", "帮我看余额再签到", "不要查询我的余额", "“帮我看一下我的余额”是什么意思", "请解释帮我看一下我的余额", "帮我看一下我的余额\n然后转账给别人", "帮我修改余额", "我还有多少次抽奖然后抽一次"} {
		t.Run(request, func(t *testing.T) { require.Empty(t, naturalAccountCommand(request)) })
	}
}

func TestNaturalBalanceRequestsReadSenderWithoutChatQuotaOrModels(t *testing.T) {
	for _, eventType := range []string{groupAtMessageCreate, groupMessageCreate} {
		for _, state := range []string{"bound", "unbound", "read failure"} {
			t.Run(eventType+"/"+state, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				t.Cleanup(func() { _ = db.Close() })
				query := mock.ExpectQuery(regexp.QuoteMeta(`SELECT u.balance FROM qq_bot_bindings b JOIN users u ON u.id=b.user_id WHERE b.app_id=$1 AND b.member_id=$2 AND u.deleted_at IS NULL AND u.status='active'`)).WithArgs("app", "sender")
				want := "USD 12.50"
				switch state {
				case "bound":
					query.WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(12.5))
				case "unbound":
					query.WillReturnError(sql.ErrNoRows)
					want = bindFirst
				case "read failure":
					query.WillReturnError(sql.ErrConnDone)
					want = "账户信息查询失败，请稍后重试。"
				}
				mock.ExpectExec("INSERT INTO qq_bot_message_references").WithArgs("app", "group", "sent", "", sqlmock.AnyArg(), false, false, "", "").WillReturnResult(sqlmock.NewResult(0, 1))
				var replies []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/token":
						_, _ = w.Write([]byte(`{"access_token":"local-test","expires_in":7200}`))
					case "/v2/groups/group/bot_state":
						_, _ = w.Write([]byte(`{"member_openid":"this-bot"}`))
					case "/v2/groups/group/messages":
						replies = append(replies, readQQTestReply(t, r).text())
						_, _ = w.Write([]byte(`{"id":"sent"}`))
					default:
						t.Errorf("personal query called unexpected endpoint: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				t.Cleanup(server.Close)
				s := New(db, nil, false, nil)
				s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
				c := DefaultConfig()
				c.AppID, c.BaseURL, c.ChatDisabled = "app", server.URL+"/v1", true
				c.DailyChatLimit, c.TotalDailyChatLimit = 0, 0
				e := Event{GroupID: "group", Content: "<@this-bot> 帮我看一下我的余额"}
				e.Author.MemberID = "sender"
				j := Job{AppID: c.AppID, EventType: eventType, GroupID: "group", MemberID: "sender", MessageID: "question", CreatedAt: time.Now()}
				require.NoError(t, s.message(context.Background(), c, &j, e))
				require.Len(t, replies, 1)
				require.Contains(t, replies[0], want)
				require.False(t, j.ShareHistory)
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}

func TestPlainCommandsAndPersonalRequestsStayOutOfHistoryAndMemberHints(t *testing.T) {
	custom := []CustomCommand{{Command: "/faq", Reply: "fixed", Enabled: true}}
	for _, text := range []string{"绑定 secret-code", "<@bot> 转账 10 recipient@example.test", "我的余额", "你好\n绑定 private-code", "帮我看一下我的余额", "faq private-argument", "福利 兑换 SECRET", "奖励兑换 SECRET"} {
		e := Event{Content: text}
		e.Author.MemberID = "sender"
		require.False(t, shareableGroupEvent(e, nil, custom...), text)
		_, hint := groupMemberHint(e, custom...)
		require.Empty(t, hint, text)
		require.Empty(t, memberDirectoryPreview(text, custom...), text)
	}
	for _, text := range []string{"帮我解释签到的规则", "余额不足该怎么办", "这张菜单设计怎么样", "我的 代码报错了", "help me"} {
		require.True(t, shareableGroupEvent(Event{Content: text}, nil, custom...), text)
	}
}
