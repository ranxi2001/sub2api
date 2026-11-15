package qqbot

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRankingConfigValidation(t *testing.T) {
	cfg := defaultRankingConfig()
	require.True(t, cfg.Enabled)
	require.False(t, cfg.RewardsEnabled)
	require.NoError(t, validateRankingConfig(cfg))
	cfg.RewardsEnabled = true
	require.Error(t, validateRankingConfig(cfg))
	cfg.TokenRewards = []RankingPrize{{Rank: 10, Reward: GrowthReward{Type: "balance", Amount: 0.00000001}}}
	require.NoError(t, validateRankingConfig(cfg))
	for _, rank := range []int{0, -1, 11} {
		cfg.TokenRewards[0].Rank = rank
		require.Error(t, validateRankingConfig(cfg))
	}
	cfg.TokenRewards[0].Rank = 1
	cfg.TokenRewards = append(cfg.TokenRewards, cfg.TokenRewards[0])
	require.Error(t, validateRankingConfig(cfg))
	cfg.TokenRewards = cfg.TokenRewards[:1]
	cfg.TokenRewards[0].Reward = GrowthReward{Type: "physical", Quantity: 1}
	require.Error(t, validateRankingConfig(cfg))
	cfg.TokenRewards[0].Reward.PrizeName = "Prize"
	require.NoError(t, validateRankingConfig(cfg))
	cfg.TokenRewards[0].Reward = GrowthReward{Type: "balance", Amount: 1001}
	require.Error(t, validateRankingConfig(cfg))
	cfg.RewardsEnabled = false
	cfg.TokenRewards = nil
	cfg.AmountRewards = nil
	require.NoError(t, validateRankingConfig(cfg))
	normalizeRankingPrizes(&cfg)
	require.NotNil(t, cfg.TokenRewards)
	require.NotNil(t, cfg.AmountRewards)
}

func TestMaskRankingEmail(t *testing.T) {
	for _, tc := range []struct {
		email string
		want  string
	}{
		{"123456789@qq.com", "123****89@qq.com"},
		{"abcdef@example.com", "abc****ef@example.com"},
		{"abcde@example.com", "a****e@example.com"},
		{"abcd@example.com", "a****d@example.com"},
		{"abc@example.com", "a****c@example.com"},
		{"ab@example.com", "****@example.com"},
		{"a@example.com", "****@example.com"},
		{"用户测试邮箱@example.com", "用户测****邮箱@example.com"},
		{" 123456789@qq.com ", "123****89@qq.com"},
		{"", "邮箱不可用"},
		{"private-address", "邮箱不可用"},
		{"@example.com", "邮箱不可用"},
		{"private@", "邮箱不可用"},
		{"private@example.com@other.com", "邮箱不可用"},
		{"private@example.com\n**second row**", "邮箱不可用"},
	} {
		t.Run(tc.email, func(t *testing.T) {
			require.Equal(t, tc.want, maskRankingEmail(tc.email))
			if tc.want != "邮箱不可用" {
				require.NotEqual(t, strings.TrimSpace(tc.email), tc.want)
			}
		})
	}
}

func TestFormatTodayRankingsUsesUniformLiteralEmailRows(t *testing.T) {
	var entries []rankingEntry
	var wantRows []string
	for _, kind := range []string{"tokens", "amount"} {
		for rank := 1; rank <= rankingTopN; rank++ {
			email, label := "123456789@qq.com", `123\*\*\*\*89@qq.com`
			if rank%2 == 0 {
				email, label = "a_b_private_cd@example.com", `a\_b\*\*\*\*cd@example.com`
			}
			entries = append(entries, rankingEntry{Kind: kind, Rank: rank, UserID: int64(rank), Email: email})
			wantRows = append(wantRows, fmt.Sprintf("%d. %s", rank, label))
		}
	}
	text := formatTodayRankings("2026-10-21", entries)
	require.Contains(t, text, "**Token 消耗榜**")
	require.Contains(t, text, "**消费金额榜**")
	require.Contains(t, text, "仅展示脱敏邮箱和名次")
	require.NotContains(t, text, "123456789")
	require.NotContains(t, text, "private")
	var rows []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "@") {
			rows = append(rows, line)
			// No unescaped emphasis markers can change a row or its neighbors.
			literal := strings.ReplaceAll(strings.ReplaceAll(line, `\*`, ""), `\_`, "")
			require.NotContains(t, literal, "*")
			require.NotContains(t, literal, "_")
		}
	}
	require.Equal(t, wantRows, rows)
	require.Equal(t, 2, strings.Count(formatTodayRankings("2026-10-21", nil), "暂无上榜账号。"))
}

func TestRankingBeijingDayAndCommands(t *testing.T) {
	at, err := time.Parse(time.RFC3339, "2026-10-21T16:00:00Z")
	require.NoError(t, err)
	require.Equal(t, "2026-10-22T00:00:00+08:00", beijingDayStart(at).Format(time.RFC3339))
	require.True(t, isBuiltinCommand("今日排行榜"))
	require.True(t, isBuiltinCommand("今日排行版"))
	require.False(t, isBuiltinCommand("今日Token排行"))
	require.Error(t, validateCustomCommands([]CustomCommand{{Command: "/今日排行榜", Reply: "override", Enabled: true}}))
}
