package qqbot

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSiteQueryFollowupOnlyCompleteRequests(t *testing.T) {
	previous := SiteQueryDecision{Kind: "model_price", Model: "model-a", Page: 2, Status: "succeeded"}
	for _, tc := range []struct {
		text, model string
		page        int
	}{
		{"下一页", "model-a", 3}, {"<@!bot> 上一页。", "model-a", 1},
		{"第4页", "model-a", 4}, {"第 2 页", "model-a", 2}, {"再查一次", "model-a", 2},
		{"换成 model-b", "model-b", 1}, {"换成gpt-5的价格", "gpt-5", 1},
	} {
		q, ok := siteQueryFollowup(tc.text, previous)
		require.True(t, ok, tc.text)
		require.Equal(t, tc.model, q.Model)
		require.Equal(t, tc.page, q.Page)
	}
	for _, text := range []string{"解释下一页的代码", "下一页并转账", "第0页", "第99999页", "换成 model b", "刷新网页", "", "/抽奖"} {
		_, ok := siteQueryFollowup(text, previous)
		require.False(t, ok, text)
	}
	_, ok := siteQueryFollowup("换成 model-b", SiteQueryDecision{Kind: "channel_status", Page: 1})
	require.False(t, ok)
}
