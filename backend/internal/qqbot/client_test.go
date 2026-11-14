package qqbot

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQWebhookSignatureAndChallenge(t *testing.T) {
	secret := "test-secret"
	key, err := signingKey(secret)
	require.NoError(t, err)
	raw := []byte(`{"op":0,"t":"GROUP_AT_MESSAGE_CREATE"}`)
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := hex.EncodeToString(ed25519.Sign(key, append([]byte(ts), raw...)))
	require.True(t, VerifySignature(secret, ts, sig, raw, now))
	require.False(t, VerifySignature(secret, ts, sig, append(raw, ' '), now))
	require.False(t, VerifySignature(secret, ts, sig, raw, now.Add(6*time.Minute)))
	require.False(t, VerifySignature("", ts, sig, raw, now))
	// Published QQ callback vector verifies key derivation and concatenation.
	got, err := Challenge("DG5g3B4j9X2KOErG", json.RawMessage(`{"plain_token":"Arq0D5A61EgUu4OxUvOp","event_ts":"1725442341"}`))
	require.NoError(t, err)
	require.Equal(t, "87befc99c42c651b3aac0278e71ada338433ae26fcb24307bdc5ad38c1adc2d01bcfcadc0842edac85e85205028a1132afe09280305f13aa6909ffc2d652c706", got["signature"])
	for _, token := range []string{`{"op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{}}`, "\n{\"op\":0}", "{}", "[]"} {
		data, marshalErr := json.Marshal(map[string]string{"plain_token": token, "event_ts": ts})
		require.NoError(t, marshalErr)
		_, err = Challenge(secret, data)
		require.Error(t, err, "unsigned validation must not sign a forgeable event body")
	}
}

func TestChatProtocolsUseOnlyOperatorCredentials(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer operator-test-key", r.Header.Get("Authorization"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, "station-model", body["model"])
				require.Equal(t, false, body["stream"])
				if protocol == "responses" {
					require.Equal(t, "/v1/responses", r.URL.Path)
					require.Equal(t, false, body["store"])
					_, _ = w.Write([]byte(`{"status":"completed","output":[{"content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":12,"output_tokens":3}}`))
				} else {
					require.Equal(t, "/v1/chat/completions", r.URL.Path)
					_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`))
				}
			}))
			defer server.Close()
			s := New(nil, nil, false, nil)
			c := DefaultConfig()
			c.BaseURL = server.URL + "/v1"
			c.APIKey = "operator-test-key"
			c.ChatModel = "station-model"
			c.ChatProtocol = protocol
			text, in, out, err := s.chat(context.Background(), c, []Message{{Role: "user", Content: "question"}})
			require.NoError(t, err)
			require.Equal(t, "hello", text)
			require.EqualValues(t, 12, in)
			require.EqualValues(t, 3, out)
		})
	}
}
func TestChatStreamCompleteness(t *testing.T) {
	raw := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"output_text\":\"hello\",\"usage\":{\"input_tokens\":7,\"output_tokens\":1}}}\n\n")
	text, in, out, err := parseChat(raw, "text/event-stream")
	require.NoError(t, err)
	require.Equal(t, "hello", text)
	require.EqualValues(t, 7, in)
	require.EqualValues(t, 1, out)
	_, _, _, err = parseChat([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n"), "text/event-stream")
	require.Error(t, err)
	_, _, _, err = parseChat([]byte(`{"status":"incomplete","output_text":"partial"}`), "application/json")
	require.Error(t, err)
}
func TestImagesGenerateThenReuseSavedBytesForDelivery(t *testing.T) {
	var picture bytes.Buffer
	require.NoError(t, png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 32, 32))))
	generates, uploads, messages := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/images/generations":
			generates++
			require.Equal(t, "Bearer operator-image-key", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(picture.Bytes())}}})
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"qq-token","expires_in":"7200"}`))
		case "/v2/groups/group/files":
			uploads++
			require.Equal(t, "QQBot qq-token", r.Header.Get("Authorization"))
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, false, body["srv_send_msg"])
			require.NotEmpty(t, body["file_data"])
			_, _ = w.Write([]byte(`{"file_info":"uploaded"}`))
		case "/v2/groups/group/messages":
			messages++
			body := readQQTestReply(t, r)
			require.Equal(t, 7, body.MsgType)
			require.Equal(t, "uploaded", body.Media.FileInfo)
			require.Equal(t, "msg", body.MsgID)
			require.Equal(t, messages+1, body.Sequence)
			_, _ = w.Write([]byte(`{"id":"sent"}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	s := New(nil, nil, false, nil)
	s.qqURL = server.URL
	s.tokenURL = server.URL + "/token"
	c := DefaultConfig()
	c.BaseURL = server.URL + "/v1"
	c.APIKey = "operator-image-key"
	c.AppID = "app"
	c.AppSecret = "qq-secret"
	img, err := s.generateImage(context.Background(), c, "cat")
	require.NoError(t, err)
	j := Job{EventType: groupMessageCreate, GroupID: "group", MemberID: "member", MessageID: "msg", CreatedAt: time.Now()}
	require.NoError(t, s.send(context.Background(), c, j, "", img, 2))
	require.NoError(t, s.send(context.Background(), c, j, "", img, 3))
	require.Equal(t, 1, generates)
	require.Equal(t, 2, uploads)
	require.Equal(t, 2, messages)
	j.CreatedAt = time.Now().Add(-6 * time.Minute)
	require.Error(t, s.send(context.Background(), c, j, "", img, 4))
	require.Equal(t, 2, uploads)
}
func TestGroupTextRepliesUseMarkdownWithConsistentMentions(t *testing.T) {
	var reply qqTestReply
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"qq-token","expires_in":"7200"}`))
		case "/v2/groups/group/messages":
			reply = readQQTestReply(t, r)
			require.Equal(t, "incoming", reply.MsgID)
			require.Equal(t, 1, reply.Sequence)
			_, _ = w.Write([]byte(`{"id":"sent"}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	s := New(nil, nil, false, nil)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	c := DefaultConfig()
	c.AppID, c.AppSecret = "app", "qq-secret"
	const memberID = "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4"
	const mention = `<qqbot-at-user id="` + memberID + `" />`
	for _, tc := range []struct {
		name, eventType, memberID, text, want string
		wantType                              int
	}{
		{"unmentioned group message", groupMessageCreate, memberID, "回复正文", mention + "\n回复正文", 2},
		{"email binding acknowledgement", groupMessageCreate, memberID, emailBindingAccepted, mention + "\n" + emailBindingAccepted, 2},
		{"help newline", groupMessageCreate, memberID, "\n指令：/帮助\n说明：查看帮助\n\n指令：/签到", mention + "\n指令：/帮助\n说明：查看帮助\n\n指令：/签到", 2},
		{"QQ automatic mention", groupAtMessageCreate, memberID, "回复正文", "回复正文", 2},
		{"automatic mention help", groupAtMessageCreate, memberID, "\n指令：/帮助", "\n指令：/帮助", 2},
		{"missing sender", groupMessageCreate, "", "回复正文", "回复正文", 2},
		{"escaped sender", groupMessageCreate, `member"<&`, "回复正文", "<qqbot-at-user id=\"member&#34;&lt;&amp;\" />\n回复正文", 2},
		{"HTML break normalized", groupAtMessageCreate, memberID, "奖励：$19.00<br />\n下一项", "奖励：$19.00\n下一项", 2},
		{"long automatic mention", groupAtMessageCreate, memberID, strings.Repeat("中", 1900), strings.Repeat("中", 1799) + "…", 2},
		{"long explicit mention", groupMessageCreate, memberID, strings.Repeat("中", 1900), mention + "\n" + strings.Repeat("中", 1799-len([]rune(mention+"\n"))) + "…", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := Job{EventType: tc.eventType, GroupID: "group", MemberID: tc.memberID, MessageID: "incoming", CreatedAt: time.Now()}
			require.NoError(t, s.send(context.Background(), c, j, tc.text, nil, 1))
			require.Equal(t, tc.wantType, reply.MsgType)
			require.Equal(t, tc.want, reply.text())
			require.NotContains(t, reply.text(), "<@")
		})
	}
}

func TestGroupMentionMarkdownRejectionDoesNotSendLiteralTagFallback(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"qq-token","expires_in":7200}`))
			return
		}
		require.Equal(t, "/v2/groups/group/messages", r.URL.Path)
		calls++
		reply := readQQTestReply(t, r)
		require.Equal(t, 2, reply.MsgType)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":40034127}`))
	}))
	defer server.Close()
	s := New(nil, nil, false, nil)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	c := DefaultConfig()
	c.AppID, c.AppSecret = "app", "qq-secret"
	j := Job{EventType: groupMessageCreate, GroupID: "group", MemberID: "member", MessageID: "incoming", CreatedAt: time.Now()}
	require.ErrorContains(t, s.send(context.Background(), c, j, emailBindingAccepted, nil, 1), "40034127")
	require.Equal(t, 1, calls)
}

func TestUpstreamErrorsDoNotExposeCredentialsOrRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(401)
		_, _ = w.Write([]byte("secret-token upstream details"))
	}))
	defer server.Close()
	s := New(nil, nil, false, nil)
	c := DefaultConfig()
	c.BaseURL = server.URL
	c.APIKey = "operator-key"
	_, _, _, err := s.chat(context.Background(), c, nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-token")
	require.Equal(t, 1, calls)
}
func TestConfigAndCommandBoundaries(t *testing.T) {
	c := DefaultConfig()
	require.NoError(t, validateConfig(c))
	c.Enabled = true
	require.Error(t, validateConfig(c))
	c = DefaultConfig()
	c.APIKey = "secret"
	c.AppSecret = "qq-secret"
	p := c.Public()
	require.Empty(t, p.APIKey)
	require.Empty(t, p.AppSecret)
	require.True(t, p.APIKeySet)
	c.CheckinReward = -1
	require.Error(t, validateConfig(c))
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1"} {
		require.False(t, publicIP(net.ParseIP(ip)), ip)
	}
	require.True(t, publicIP(net.ParseIP("8.8.8.8")))
	_, err := downloadImage(context.Background(), "https://127.0.0.1/test")
	require.Error(t, err)
	require.Equal(t, "2026-10-16", day(time.Date(2026, 10, 15, 16, 0, 0, 0, time.UTC)))
	require.NotContains(t, strings.Join([]string{p.APIKey, p.AppSecret}, ""), "secret")
}

func TestCommandAcceptsKnownNamesAndSlashCompatibility(t *testing.T) {
	for _, tt := range []struct {
		content string
		name    string
		arg     string
	}{
		{content: "/帮助", name: "帮助"},
		{content: "  <@123> /签到  ", name: "签到"},
		{content: "<@!123>/生图 一只猫 在月球上", name: "生图", arg: "一只猫 在月球上"},
		{content: "<@123> /绑定\talice@example.com  ", name: "绑定", arg: "alice@example.com"},
		{content: "/绑定\nbinding-code", name: "绑定", arg: "binding-code"},
		{content: "<@123> 签到", name: "签到"},
		{content: "生图 一只猫", name: "生图", arg: "一只猫"},
		{content: "帮助", name: "帮助"},
		{content: "我的余额", name: "我的余额"},
		{content: "/我的余额", name: "我的余额"},
		{content: "余额", name: "我的余额"},
		{content: "/余额", name: "我的余额"},
		{content: "<@123> 余额", name: "我的余额"},
		{content: "余额 多余参数", name: "我的余额", arg: "多余参数"},
		{content: "抽奖", name: "抽奖"},
		{content: "抽奖 10", name: "抽奖", arg: "10"},
		{content: "/抽奖 10", name: "抽奖", arg: "10"},
		{content: "  抽奖   10  ", name: "抽奖", arg: "10"},
		{content: "抽奖\u300010", name: "抽奖", arg: "10"},
		{content: "<@123> 抽奖 10", name: "抽奖", arg: "10"},
		{content: "绑定\nbinding-code", name: "绑定", arg: "binding-code"},
		{content: "转账 1.25 alice@example.com", name: "转账", arg: "1.25 alice@example.com"},
		{content: "<@123> faq", name: "faq"},
		{content: "<@123> 停用", name: "停用"},
		{content: "faq 参数", name: "faq", arg: "参数"},
		{content: "/未知 参数", name: "未知", arg: "参数"},
		{content: "未知 参数"},
		{content: "签到功能怎么用"},
		{content: "余额不足该怎么办"},
		{content: "抽奖10"},
		{content: "帮我抽奖 10 次"},
		{content: "我的 余额"},
		{content: "我的 代码报错了"},
		{content: "help me"},
		{content: "帮我解释 /签到 的作用"},
		{content: "<@123> "},
	} {
		t.Run(tt.content, func(t *testing.T) {
			name, arg := command(tt.content, CustomCommand{Command: "/faq", Enabled: true}, CustomCommand{Command: "/停用"})
			require.Equal(t, tt.name, name)
			require.Equal(t, tt.arg, arg)
		})
	}
}

func TestRemovedAndUnknownCommandsDoNotCallModelsOrAccessData(t *testing.T) {
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
			t.Errorf("unsupported command called unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	// No database is provided: unsupported commands must not read saved results,
	// delete conversations, reserve model usage, or invoke an upstream model.
	s := New(nil, nil, false, nil)
	s.qqURL = server.URL
	s.tokenURL = server.URL + "/token"
	c := DefaultConfig()
	c.AppID, c.AppSecret, c.BaseURL = "app", "test-secret", server.URL+"/v1"
	for i, content := range []string{"/查看结果 task-id", "/新对话", "/清空", "/不存在"} {
		j := Job{GroupID: "group", MessageID: strconv.Itoa(i), CreatedAt: time.Now()}
		require.NoError(t, s.message(context.Background(), c, &j, Event{Content: content}))
		require.Len(t, replies, i+1)
		require.Contains(t, replies[i], "帮助")
	}
	j := Job{GroupID: "group", MessageID: "help", CreatedAt: time.Now()}
	before := len(replies)
	require.NoError(t, s.message(context.Background(), c, &j, Event{Content: "/帮助"}))
	help := strings.Join(replies[before:], "\n")
	require.Contains(t, help, "我的余额")
	require.Contains(t, help, "成长任务")
	for _, removed := range []string{"/查看结果", "/新对话", "/清空"} {
		require.NotContains(t, help, removed)
	}
}
