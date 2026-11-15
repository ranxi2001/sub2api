package qqbot

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupMemberHint(t *testing.T) {
	e := Event{Content: "<@bot> 我是小王，\n确认身份 4826"}
	e.Author.MemberID, e.Author.Username = "member", " 小王\n同学 "
	nickname, preview := groupMemberHint(e)
	require.Equal(t, "小王 同学", nickname)
	require.Equal(t, "我是小王， 确认身份 4826", preview)
	e.Author.Username = ""
	_, preview = groupMemberHint(e)
	require.Contains(t, preview, "4826", "identification must work when QQ omits the nickname")
	for _, content := range []string{"/绑定 secret", "<@bot> /福利 兑换 SECRET", "请看这里\n/转账 secret"} {
		e.Content = content
		_, preview = groupMemberHint(e)
		require.Empty(t, preview, "never retain command contents")
	}
	e.Content, e.MessageReference.MessageID = "quoted secret", "quote"
	_, preview = groupMemberHint(e)
	require.Empty(t, preview)
	e.MessageReference.MessageID = ""
	e.MsgElements = []MessageElement{{MessageType: 103}}
	_, preview = groupMemberHint(e)
	require.Empty(t, preview)
	e.MsgElements = nil
	e.Content = strings.Repeat("中", 200)
	_, preview = groupMemberHint(e)
	require.Len(t, []rune(preview), 100)
	e.Author.Username = strings.Repeat("名", 200)
	nickname, _ = groupMemberHint(e)
	require.Len(t, []rune(nickname), 100)
}
