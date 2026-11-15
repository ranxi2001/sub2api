package qqbot

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemberDirectoryFilterRequiresExactGroupForSelectedIdentity(t *testing.T) {
	valid := MemberDirectoryFilter{Page: 1, PageSize: 20}
	require.NoError(t, validateMemberDirectoryFilter(valid))
	for _, f := range []MemberDirectoryFilter{
		{Page: 0, PageSize: 20}, {Page: 1, PageSize: 101},
		{Page: 1, PageSize: 20, MemberID: "selected"},
		{Page: 1, PageSize: 20, GroupID: "bad group"},
		{Page: 1, PageSize: 20, Search: strings.Repeat("字", 101)},
		{Page: 1, PageSize: 20, Search: "line\nbreak"},
	} {
		require.Error(t, validateMemberDirectoryFilter(f))
	}
}

func TestMemberDirectoryPrivacyGuardDoesNotExposeCommands(t *testing.T) {
	require.Empty(t, memberDirectoryPreview("/绑定 123456"))
	require.Empty(t, memberDirectoryPreview("上面的命令\n<@bot> /绑定 ABC-SECRET"))
	require.Equal(t, "正常成员简介", memberDirectoryPreview(" 正常成员简介 "))
}

func TestModerationExemptionNicknameIsOnlyADisplayLabel(t *testing.T) {
	c := DefaultConfig()
	c.AppID = "app"
	c.AllowedGroups = []string{"group"}
	c.Moderation.Groups = []string{"group"}
	c.Moderation.Exemptions = normalizeModerationExemptions([]ModerationExemption{{GroupID: " group ", MemberID: " actual-id ", Nickname: " 备注昵称 "}})
	require.Equal(t, ModerationExemption{GroupID: "group", MemberID: "actual-id", Nickname: "备注昵称"}, c.Moderation.Exemptions[0])
	require.NoError(t, validateModeration(c))
	require.True(t, c.moderationExempt("group", "actual-id"))
	require.False(t, c.moderationExempt("group", "备注昵称"))
	c.Moderation.Exemptions = append(c.Moderation.Exemptions, ModerationExemption{GroupID: "group", MemberID: "actual-id", Nickname: "不同备注"})
	require.Error(t, validateModeration(c), "different labels must not bypass identity deduplication")
	c.Moderation.Exemptions = []ModerationExemption{{GroupID: "group", MemberID: "actual-id", Nickname: strings.Repeat("字", 101)}}
	require.Error(t, validateModeration(c))
	c.Moderation.Exemptions[0].Nickname = "含\n换行"
	require.Error(t, validateModeration(c))
}
