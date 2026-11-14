package qqbot

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpamReportCallbackFingerprintAcceptsOnlyDocumentedCrossEventDifference(t *testing.T) {
	c := DefaultConfig()
	c.AppSecret = "test-only"
	for _, tc := range []struct {
		name, full, at string
		match          bool
	}{
		{"same_body", "原文", "原文", true},
		{"removed_bot_mention", "<@BOT123> 原文", " 原文 ", true},
		{"bang_mention", "<@!BOT123> 原文", "原文", true},
		{"different_body", "<@BOT123> 广告", "正常消息", false},
		{"interior_mention", "原文 <@BOT123>", "原文", false},
		{"all_mention", "<@all> 原文", "原文", false},
		{"multiple_mentions", "<@BOT123> <@OTHER> 原文", "原文", false},
		{"empty_mention", "<@> 原文", "原文", false},
		{"invalid_mention", "<@NOT A MEMBER> 原文", "原文", false},
		{"whitespace_without_mention", " 原文 ", "原文", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full, at := spamSourceTestEvent("message"), spamSourceTestEvent("message")
			full.Content, at.Content = tc.full, tc.at
			a := spamReportCallbackFingerprint(c, full, groupMessageCreate, true)
			b := spamReportCallbackFingerprint(c, at, groupAtMessageCreate, true)
			require.Equal(t, tc.match, spamReportCallbacksMatch(a, b))
			require.Equal(t, tc.match, spamReportCallbacksMatch(b, a))
			require.True(t, spamReportCallbacksMatch(a, a))
			require.True(t, spamReportCallbacksMatch(b, b))
			if tc.full != tc.at {
				b.Kind = "full"
				require.False(t, spamReportCallbacksMatch(a, b), "same-event body changes remain conflicts")
			}
			require.False(t, spamReportCallbacksMatch(a, spamReportCallbackHash{}))
			require.False(t, spamReportCallbacksMatch(a, spamReportCallbackHash{Version: 1, Kind: "invalid"}))
		})
	}
}
