package qqbot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetiredRedemptionRedactsAllPayloadCopies(t *testing.T) {
	code := "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"
	for _, prefix := range []string{"/福利 兑换 ", "<@bot> /福利\t兑换\n", "/奖励兑换 "} {
		e := Event{Content: prefix + code, MsgElements: []MessageElement{{Content: code}}, Attachments: []Attachment{{URL: code}}, MessageScene: MessageScene{Ext: []string{code}}}
		require.True(t, containsRewardCode(e.Content))
		clean := redactRewardCommand(e)
		raw, err := json.Marshal(clean)
		require.NoError(t, err)
		require.NotContains(t, string(raw), code)
		require.False(t, isBuiltinOrCustomCommand(clean, nil))
	}
}

func TestCommandPaginationPreservesLongRules(t *testing.T) {
	paragraphs := make([]string, 30)
	for i := range paragraphs {
		paragraphs[i] = strings.Repeat("成长任务", 50)
	}
	body := strings.Join(paragraphs, "\n\n")
	pages := paginateCommand(body)
	require.Equal(t, body, strings.Join(pages, "\n\n"))
	for _, page := range pages {
		require.LessOrEqual(t, len([]rune(page)), 1300)
	}
	long := strings.Repeat("规则\n", 1800)
	require.Equal(t, long, strings.Join(paginateCommand(long), ""))
}
