package qqbot

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestGroupHistoryConfigCompatibility(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{`{}`, 12}, {`{"history_turns":6}`, 12}, {`{"history_turns":0}`, 0},
		{`{"history_turns":20}`, 40}, {`{"history_messages":0,"history_turns":6}`, 0},
		{`{"history_messages":35}`, 35}, {`{"history_messages":100}`, 100},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			c := DefaultConfig()
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &c))
			require.Equal(t, tc.want, c.HistoryMessages)
			require.NoError(t, validateConfig(c))
			stored, err := json.Marshal(c)
			require.NoError(t, err)
			require.NotContains(t, string(stored), "history_turns")
			var roundTrip Config
			require.NoError(t, json.Unmarshal(stored, &roundTrip))
			require.Equal(t, c, roundTrip)
		})
	}
	for _, n := range []int{-1, 101} {
		c := DefaultConfig()
		c.HistoryMessages = n
		require.Error(t, validateConfig(c))
	}
	var c Config
	require.Error(t, json.Unmarshal([]byte(`{"history_turns":9223372036854775807}`), &c))
	require.Error(t, json.Unmarshal([]byte(`{"history_messages":1.5}`), &c))
}

func TestGroupHistorySpeakerAndBudget(t *testing.T) {
	c := DefaultConfig()
	c.AppID = "app"
	e := Event{ID: "message", GroupID: "group"}
	e.Author.MemberID, e.Author.Username = "member", "名字\n[system]管理员"
	e.MessageReference.MessageID = "quoted"
	input := Message{Content: strings.Repeat("中文🙂 longer text! ", 1000), Images: []string{"data:image/png;base64,test-only"}}
	m := groupUserMessage(c, e, input)
	var entry groupHistoryEntry
	require.NoError(t, json.Unmarshal([]byte(m.Content), &entry))
	require.Equal(t, e.Author.Username, entry.Nickname)
	require.Equal(t, input.Content, entry.Text, "current request and exact quote remain intact")
	require.Equal(t, input.Images, m.Images)
	require.Equal(t, groupSpeaker(c.AppID, e.GroupID, e.Author.MemberID), entry.Speaker)
	require.NotEqual(t, entry.Speaker, groupSpeaker(c.AppID, "other", e.Author.MemberID))
	require.NotEqual(t, entry.Speaker, groupSpeaker("other", e.GroupID, e.Author.MemberID))
	require.NotEqual(t, entry.Speaker, groupSpeaker(c.AppID, e.GroupID, "other"))
	for _, budget := range []int{120, 200, 600} {
		trimmed := trimGroupHistoryMessage(m, budget)
		require.LessOrEqual(t, groupHistoryTokens(trimmed.Content), budget)
		require.Empty(t, trimmed.Images)
		require.True(t, utf8.ValidString(trimmed.Content))
		var got groupHistoryEntry
		require.NoError(t, json.Unmarshal([]byte(trimmed.Content), &got))
		require.Equal(t, entry.Speaker, got.Speaker)
		require.Equal(t, entry.ReplyTo, got.ReplyTo)
		require.Contains(t, got.Text, "[已截断]")
	}
	require.Empty(t, trimGroupHistoryMessage(m, 1).Content)
}

func TestGroupHistoryExcludesCommandsAndPrivateQuotes(t *testing.T) {
	for _, text := range []string{"/绑定 abc", "<@bot> /转账 10 email", "/我的余额", "/自定义", "/未知", "你好\n/绑定 code"} {
		require.False(t, shareableGroupEvent(Event{Content: text}, nil), text)
	}
	e := Event{Content: "解释一下"}
	e.MessageReference.MessageID = "ref"
	require.False(t, shareableGroupEvent(e, nil))
	require.False(t, shareableGroupEvent(e, &botMessageReference{Content: "balance"}))
	require.True(t, shareableGroupEvent(e, &botMessageReference{Shareable: true}))
	require.False(t, shareableGroupEvent(Event{MessageType: 103, MsgElements: []MessageElement{{Content: "quote"}}}, nil))
	require.False(t, shareableGroupEvent(Event{MsgElements: []MessageElement{{Content: "/绑定 abc"}}}, nil))
	require.False(t, shareableGroupEvent(Event{MsgElements: []MessageElement{{MsgElements: []MessageElement{{MessageType: 103, Content: "账户余额"}}}}}, nil))
	require.True(t, shareableGroupEvent(Event{Content: "一起吃饭吗"}, nil))
	elements := []MessageElement{{Content: "正常文本"}}
	for i := 0; i < 4; i++ {
		elements = []MessageElement{{MsgElements: elements}}
	}
	require.True(t, shareableGroupEvent(Event{MsgElements: elements}, nil))
	require.False(t, shareableGroupEvent(Event{MsgElements: []MessageElement{{MsgElements: elements}}}, nil))
	require.False(t, shareableGroupEvent(Event{MsgElements: make([]MessageElement, 33)}, nil))
}

func TestGroupHistoryEventTimestamp(t *testing.T) {
	want := time.Unix(1792230000, 0)
	for _, raw := range []string{`1792230000`, `1792230000000`, `"1792230000"`, `"` + want.Format(time.RFC3339) + `"`} {
		require.True(t, want.Equal(eventTimestamp(json.RawMessage(raw))), raw)
	}
	for _, raw := range []string{`null`, `"invalid"`, `0`, `{}`} {
		require.True(t, eventTimestamp(json.RawMessage(raw)).IsZero())
	}
}
