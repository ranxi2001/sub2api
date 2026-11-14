package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

type previewEncryptor struct{}

func (previewEncryptor) Encrypt(value string) (string, error) { return value, nil }
func (previewEncryptor) Decrypt(value string) (string, error) { return value, nil }

func previewTestConfig() Config {
	c := DefaultConfig()
	c.Enabled, c.AppID, c.AllowedGroups = true, "app", []string{"group"}
	c.BaseURL, c.APIKey, c.ChatModel = "https://example.invalid", "private-key", "test-model"
	return c
}

func expectPreviewConfig(t *testing.T, mock sqlmock.Sqlmock, c Config) {
	t.Helper()
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	mock.ExpectQuery("SELECT value FROM settings").WithArgs(configKey).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(string(raw)))
}

func TestChatPreviewHelpKeepsOptionalArgumentsAndExcludesRetiredCommands(t *testing.T) {
	c := DefaultConfig()
	c.CustomCommands = []CustomCommand{
		{Command: "/举报垃圾信息", Enabled: true},
		{Command: "/faq_test", Enabled: true},
	}
	help := helpText(c)
	require.Contains(t, help, "**抽奖 [次数]**：")
	require.NotContains(t, help, `\[次数\]`)
	require.NotContains(t, help, "举报垃圾信息")
	require.Contains(t, help, `**faq\_test**：自定义回复`, "operator-defined command labels remain escaped")
}

func TestCommandInventoryExcludesRetiredCustomCommands(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	c := previewTestConfig()
	c.CustomCommands = []CustomCommand{
		{Command: "/举报垃圾信息", Reply: "retired", Enabled: true},
		{Command: "/faq", Reply: "current", Enabled: true},
	}
	expectPreviewConfig(t, mock, c)
	commands, err := New(db, previewEncryptor{}, true, nil).Commands(context.Background())
	require.NoError(t, err)
	byCommand := make(map[string]CommandInfo, len(commands))
	for _, item := range commands {
		require.False(t, strings.HasPrefix(item.Command, "/"))
		byCommand[item.Command] = item
	}
	require.NotContains(t, byCommand, "举报垃圾信息")
	require.Equal(t, "抽奖 [次数]", byCommand["抽奖"].Usage)
	require.Equal(t, "current", byCommand["faq"].Reply)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestChatPreviewRequestValidationPrecedesAllIO(t *testing.T) {
	s := New(nil, nil, false, nil)
	for _, req := range []ChatPreviewRequest{
		{}, {GroupID: "group", Question: " "}, {GroupID: "group", Question: strings.Repeat("中", 6001)},
		{GroupID: "group other", Question: "你好"}, {GroupID: "group", MemberID: "member\n", Question: "你好"},
		{GroupID: "group", Question: "/签到"}, {GroupID: "group", Question: "/转账 1 a@example.test"}, {GroupID: "group", Question: "/帮助 2"},
	} {
		_, err := s.PreviewChat(context.Background(), req)
		require.Error(t, err)
	}
}

func TestChatPreviewConfigPolicyAndConcurrentTrials(t *testing.T) {
	for _, reason := range []string{"disabled", "group", "chat", "quota"} {
		t.Run(reason, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			c := previewTestConfig()
			switch reason {
			case "disabled":
				c.Enabled = false
			case "group":
				c.AllowedGroups = nil
			case "chat":
				c.ChatDisabled = true
			case "quota":
				c.DailyChatLimit = 0
			}
			expectPreviewConfig(t, mock, c)
			_, err = New(db, previewEncryptor{}, true, nil).PreviewChat(context.Background(), ChatPreviewRequest{GroupID: "group", Question: "你好"})
			require.Error(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
	s := New(nil, nil, false, nil)
	activeChatPreviews.Store(s, struct{}{})
	defer activeChatPreviews.Delete(s)
	_, err := s.PreviewChat(context.Background(), ChatPreviewRequest{GroupID: "group", Question: "你好"})
	require.ErrorContains(t, err, "已有试问")
}

func TestChatPreviewMenuAndReadOnlyToolsAcrossProtocols(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, scenario := range []string{"menu", "chat", "query", "provider-error"} {
			t.Run(protocol+"/"+scenario, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				defer func() { _ = db.Close() }()
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var body map[string]json.RawMessage
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					key := "messages"
					if protocol == "responses" {
						key = "input"
					}
					require.Contains(t, string(body[key]), "真实指令清单 JSON")
					require.Contains(t, string(body[key]), "试问站点资料")
					require.NotContains(t, string(body["tools"]), chatImageToolName)
					require.NotContains(t, string(body[key]), "本轮提问者已由后端验证为当前群绑定的管理员")
					if scenario == "provider-error" {
						w.WriteHeader(500)
						_, _ = w.Write([]byte("private-provider-token"))
						return
					}
					if scenario == "query" {
						_, _ = w.Write(siteQueryTestResponse(t, protocol, `{"kind":"channel_status","model":"","activity":"","page":1}`, false))
						return
					}
					if protocol == "responses" {
						_, _ = w.Write([]byte(`{"status":"completed","output_text":"试问完成","usage":{"input_tokens":3,"output_tokens":2}}`))
					} else {
						_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"试问完成"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
					}
				}))
				defer server.Close()
				c := previewTestConfig()
				c.BaseURL, c.ChatProtocol, c.ImageEnabled = server.URL, protocol, true
				c.SiteKnowledge = SiteKnowledgeConfig{Enabled: true, Content: "试问站点资料"}
				expectPreviewConfig(t, mock, c)
				s := New(db, previewEncryptor{}, true, nil)
				s.SetChannelStatusProvider(channelStatusProviderFunc(func(context.Context) (*ChannelStatusReport, error) { return channelStatusFixture(), nil }))
				question := "请说明站点资料"
				if scenario == "menu" {
					question = "菜单"
				}
				out, err := s.PreviewChat(context.Background(), ChatPreviewRequest{GroupID: "group", Question: question})
				if scenario == "provider-error" {
					require.ErrorContains(t, err, "试问模型调用失败")
					require.NotContains(t, err.Error(), "private-provider-token")
					require.NotNil(t, out.ChatExecution)
					require.Equal(t, 3, out.ChatExecution.Attempts)
					require.Equal(t, 2, out.ChatExecution.Retries)
					require.False(t, out.ChatExecution.UsageComplete)
					require.Equal(t, 3, calls)
				} else {
					require.NoError(t, err)
					require.NotNil(t, out.KnowledgeSources)
					switch scenario {
					case "menu":
						require.Equal(t, "menu", out.Action)
						require.Contains(t, out.Text, "价格 模型名")
						require.Zero(t, calls)
						require.Nil(t, out.ChatExecution)
					case "chat":
						require.Equal(t, "chat", out.Action)
						require.Equal(t, "试问完成", out.Text)
						require.EqualValues(t, 3, out.InputTokens)
						require.NotNil(t, out.ChatExecution)
						require.Equal(t, 1, out.ChatExecution.Attempts)
						require.True(t, out.ChatExecution.UsageComplete)
					case "query":
						require.Equal(t, "site_query", out.Action)
						require.NotNil(t, out.Query)
						require.Contains(t, out.Text, "查询时间：")
						require.Empty(t, out.KnowledgeSources)
					}
				}
				if scenario != "menu" && scenario != "provider-error" {
					require.Equal(t, 1, calls)
				}
				require.NoError(t, mock.ExpectationsWereMet(), "trial must not write jobs, history, or member usage")
			})
		}
	}
}

func TestChatPreviewHelpRemainsAvailableWithChatDisabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	c := previewTestConfig()
	c.ChatDisabled, c.TotalDailyChatLimit, c.DailyChatLimit = true, 0, 0
	c.BaseURL, c.APIKey, c.ChatModel = "", "", ""
	expectPreviewConfig(t, mock, c)
	out, err := New(db, previewEncryptor{}, true, nil).PreviewChat(context.Background(), ChatPreviewRequest{GroupID: "group", Question: "/帮助"})
	require.NoError(t, err)
	require.Equal(t, "menu", out.Action)
	require.Contains(t, out.Text, "签到")
	require.Zero(t, out.InputTokens)
	require.NoError(t, mock.ExpectationsWereMet())
}
