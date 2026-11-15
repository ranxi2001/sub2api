//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func rankingTime(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return at
}

func rankingUsage(t *testing.T, db *sql.DB, uid int64, tokens int64, cost string, at time.Time) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO usage_logs(user_id,model,input_tokens,actual_cost,created_at) VALUES($1,'private-model',$2,$3,$4)`, uid, tokens, cost, at)
	require.NoError(t, err)
}

func TestTodayRankingsAllAccountsPrivateTopTen(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	insightsUsageTable(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	now := rankingTime(t, "2026-10-21T12:00:00+08:00")
	start := beijingDayStart(now)
	users := make([]int64, 12)
	for i := range users {
		users[i] = user(t, db)
		_, err = db.Exec(`UPDATE users SET email=$2 WHERE id=$1`, users[i], fmt.Sprintf("private-%d@example.com", i))
		require.NoError(t, err)
		rankingUsage(t, db, users[i], int64(100001+i), fmt.Sprintf("%d.1234567890", 12-i), start.Add(time.Hour))
	}
	// Unbound users participate; binding adds no extra entry or disclosed identity.
	bind(t, db, users[1], "private-qq-member")
	_, err = db.Exec(`UPDATE qq_bot_bindings SET nickname='private-nickname'`)
	require.NoError(t, err)
	disabled, deleted, zero, past, future := user(t, db), user(t, db), user(t, db), user(t, db), user(t, db)
	_, err = db.Exec(`UPDATE users SET status='disabled' WHERE id=$1;`, disabled)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE users SET deleted_at=NOW() WHERE id=$1`, deleted)
	require.NoError(t, err)
	for _, uid := range []int64{disabled, deleted} {
		rankingUsage(t, db, uid, 9999999, "999999", start)
	}
	rankingUsage(t, db, zero, 0, "0", start)
	rankingUsage(t, db, past, 9999999, "999999", start.Add(-time.Microsecond))
	rankingUsage(t, db, future, 9999999, "999999", now.Add(time.Second))
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	entries, err := dailyRankingEntries(ctx, tx, start, now)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.Len(t, entries, 20)
	require.Equal(t, rankingEntry{Kind: "amount", Rank: 1, UserID: users[0], Email: "private-0@example.com"}, entries[0])
	require.Equal(t, rankingEntry{Kind: "tokens", Rank: 1, UserID: users[11], Email: "private-11@example.com"}, entries[10])
	text, err := s.todayRankings(ctx, c, now)
	require.NoError(t, err)
	require.Contains(t, text, "**Token 消耗榜**")
	require.Contains(t, text, "**消费金额榜**")
	require.Equal(t, 2, strings.Count(text, "\n10. "))
	require.NotContains(t, text, "\n11. ")
	require.NotContains(t, text, "USD")
	require.NotContains(t, text, "private-")
	require.NotContains(t, text, "private-nickname")
	require.NotContains(t, text, "private-qq-member")
	require.Equal(t, 20, strings.Count(text, "@example.com"))
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "@example.com") {
			require.Regexp(t, `^(?:[1-9]|10)\. pri(?:\\\*){4}(?:-[0-9]|1[01])@example\.com$`, line)
		}
	}
	require.Contains(t, text, `1. pri\*\*\*\*11@example.com`)
	require.Contains(t, text, `1. pri\*\*\*\*-0@example.com`)
	empty, err := s.todayRankings(ctx, c, start)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(empty, "暂无上榜账号"))
	cfg := defaultRankingConfig()
	cfg.Enabled = false
	_, err = s.saveRankingConfigAt(ctx, cfg, now)
	require.NoError(t, err)
	text, err = s.todayRankings(ctx, c, now)
	require.NoError(t, err)
	require.Equal(t, "今日排行榜暂未开启。", text)
}

func TestRankingSettlementCreatesUnboundEntitlementsAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	insightsUsageTable(t, db)
	var invalidated atomic.Int32
	s.invalidate = func(context.Context, int64) { invalidated.Add(1) }
	a, b, c := user(t, db), user(t, db), user(t, db)
	bind(t, db, b, "bound-member")
	dayStart := rankingTime(t, "2026-10-21T00:00:00+08:00")
	cfg := defaultRankingConfig()
	cfg.RewardsEnabled = true
	cfg.TokenRewards = []RankingPrize{{1, GrowthReward{Type: "balance", Amount: 1.25}}, {2, GrowthReward{Type: "chances", Quantity: 3}}, {3, GrowthReward{Type: "chances", Quantity: 2}}}
	cfg.AmountRewards = []RankingPrize{{1, GrowthReward{Type: "physical", Quantity: 1, PrizeName: "Cup"}}, {2, GrowthReward{Type: "other", Quantity: 1, PrizeName: "Coupon", ClaimInstructions: "Contact admin"}}}
	saved, err := s.saveRankingConfigAt(ctx, cfg, dayStart.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, "2026-10-21", saved.EffectiveDay)
	rankingUsage(t, db, a, 1000, "10", dayStart)
	rankingUsage(t, db, b, 900, "20", dayStart.Add(time.Hour))
	rankingUsage(t, db, c, 800, "0", dayStart.Add(2*time.Hour))
	now := dayStart.AddDate(0, 0, 1).Add(10 * time.Minute)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.settleDueRankings(ctx, now) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, a).Scan(&balance))
	require.Equal(t, 1.25, balance)
	var count int
	require.NoError(t, db.QueryRow(`SELECT reward_count FROM qq_bot_ranking_settlements WHERE ranking_day='2026-10-21'`).Scan(&count))
	require.Equal(t, 5, count)
	require.EqualValues(t, 1, invalidated.Load())
	var chances int
	require.NoError(t, db.QueryRow(`SELECT COALESCE(SUM(chances),0) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, c).Scan(&chances))
	require.Equal(t, 2, chances)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes WHERE source_kind='ranking' AND code_hash IS NULL`).Scan(&count))
	require.Equal(t, 5, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes WHERE status='pending'`).Scan(&count))
	require.Equal(t, 2, count)
	// Finalized awards stay unchanged when late usage changes the live ordering.
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE event_type=$1 AND member_id='bound-member' AND group_id='group'`, rankingNotificationEvent).Scan(&count))
	require.Equal(t, 2, count)
	rankingUsage(t, db, c, 9999999, "9999999", dayStart.Add(3*time.Hour))
	require.NoError(t, s.settleDueRankings(ctx, now.Add(time.Hour)))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_ranking_claims`).Scan(&count))
	require.Equal(t, 5, count)
	bind(t, db, a, "newly-bound")
	require.NoError(t, s.settleDueRankings(ctx, now.Add(2*time.Hour)))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE event_type=$1`, rankingNotificationEvent).Scan(&count))
	require.Equal(t, 2, count)
	bot, err := s.Config(ctx)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, a).Scan(&balance))
	require.Equal(t, 1.25, balance)
	require.EqualValues(t, 1, invalidated.Load())
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, c).Scan(&chances))
	require.Equal(t, 2, chances)
	summary, err := s.myRewards(ctx, bot, Job{MemberID: "newly-bound"})
	require.NoError(t, err)
	require.Contains(t, summary, "排行：USD 1.25")
	codes, err := s.ListRewardCodes(ctx, "", "pending", 1)
	require.NoError(t, err)
	require.Len(t, codes.Items, 2)
	for _, code := range codes.Items {
		require.Equal(t, "ranking", code.SourceKind)
	}
	migration, err := migrations.FS.ReadFile("362_qq_bot_daily_rankings.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
}

func TestRankingSettlementUsesDatedRulesAndCatchesUp(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	insightsUsageTable(t, db)
	uid := user(t, db)
	base := rankingTime(t, "2026-10-18T12:00:00+08:00")
	cfg := defaultRankingConfig()
	cfg.RewardsEnabled = true
	cfg.TokenRewards = []RankingPrize{{1, GrowthReward{Type: "balance", Amount: 10}}}
	cfg.AmountRewards = nil
	cfg.EffectiveDay = "2000-01-01"
	saved, err := s.saveRankingConfigAt(ctx, cfg, base)
	require.NoError(t, err)
	require.Equal(t, "2026-10-19", saved.EffectiveDay)
	require.NotNil(t, saved.AmountRewards)
	cfg.TokenRewards[0].Reward.Amount = 5
	_, err = s.saveRankingConfigAt(ctx, cfg, base.AddDate(0, 0, 1))
	require.NoError(t, err)
	cfg.RewardsEnabled = false
	_, err = s.saveRankingConfigAt(ctx, cfg, base.AddDate(0, 0, 2))
	require.NoError(t, err)
	for i := range 4 {
		rankingUsage(t, db, uid, 100, "1", base.AddDate(0, 0, i))
	}
	early := rankingTime(t, "2026-10-20T00:09:59+08:00")
	require.NoError(t, s.settleDueRankings(ctx, early))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_ranking_settlements`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, s.settleDueRankings(ctx, early.Add(time.Second)))
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 10., balance)
	require.NoError(t, s.settleDueRankings(ctx, rankingTime(t, "2026-10-22T00:10:00+08:00")))
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 15., balance)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_ranking_settlements`).Scan(&count))
	require.Equal(t, 3, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_ranking_claims`).Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, s.settleDueRankings(ctx, rankingTime(t, "2026-10-22T00:20:00+08:00")))
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 15., balance)
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 15., balance)
}

func TestRankingSettlementRollsBackFailedEntitlements(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	insightsUsageTable(t, db)
	uid := user(t, db)
	start := rankingTime(t, "2026-10-21T00:00:00+08:00")
	cfg := defaultRankingConfig()
	cfg.RewardsEnabled = true
	cfg.TokenRewards = []RankingPrize{{1, GrowthReward{Type: "balance", Amount: 2}}}
	cfg.AmountRewards = []RankingPrize{{1, GrowthReward{Type: "chances", Quantity: 2}}}
	_, err := s.saveRankingConfigAt(ctx, cfg, start.Add(-time.Hour))
	require.NoError(t, err)
	rankingUsage(t, db, uid, 100, "1", start)
	_, err = db.Exec(`CREATE FUNCTION reject_ranking_ledger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test ledger failure'; END $$;
CREATE TRIGGER reject_ranking_ledger BEFORE INSERT ON qq_bot_ranking_claims FOR EACH ROW EXECUTE FUNCTION reject_ranking_ledger()`)
	require.NoError(t, err)
	now := start.AddDate(0, 0, 1).Add(10 * time.Minute)
	require.Error(t, s.settleDueRankings(ctx, now))
	for _, table := range []string{"qq_bot_ranking_settlements", "qq_bot_ranking_claims", "qq_bot_reward_codes", "qq_bot_rewards", "qq_bot_lottery_chance_grants"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
		require.Zero(t, count)
	}
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, balance)
	_, err = db.Exec(`DROP TRIGGER reject_ranking_ledger ON qq_bot_ranking_claims`)
	require.NoError(t, err)
	require.NoError(t, s.settleDueRankings(ctx, now))
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 2., balance)
}

func TestRankingTiesPrecisionCacheTokensAndDayBoundary(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	savedBot(t, db)
	insightsUsageTable(t, db)
	a, b, c := user(t, db), user(t, db), user(t, db)
	start := rankingTime(t, "2026-10-21T00:00:00+08:00")
	rankingUsage(t, db, a, 0, "1.0000000001", start)
	_, err := db.Exec(`UPDATE usage_logs SET cache_creation_tokens=50,cache_read_tokens=50,billing_type=1 WHERE user_id=$1`, a)
	require.NoError(t, err)
	rankingUsage(t, db, b, 100, "1.0000000002", start.Add(time.Hour))
	rankingUsage(t, db, c, 100, "1.0000000002", start.Add(time.Hour))
	rankingUsage(t, db, a, 9999999, "9999999", start.AddDate(0, 0, 1))
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	entries, err := dailyRankingEntries(ctx, tx, start, start.AddDate(0, 0, 1))
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.Equal(t, []rankingEntry{
		{Kind: "amount", Rank: 1, UserID: b}, {Kind: "amount", Rank: 2, UserID: c}, {Kind: "amount", Rank: 3, UserID: a},
		{Kind: "tokens", Rank: 1, UserID: a}, {Kind: "tokens", Rank: 2, UserID: b}, {Kind: "tokens", Rank: 3, UserID: c},
	}, entries)
}

func TestRankingCommandSendsBothBoardsWithoutBindingOrModelCalls(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	insightsUsageTable(t, db)
	bot, err := s.Config(ctx)
	require.NoError(t, err)
	for i, email := range []string{"123456789@qq.com", "second_person@example.com"} {
		uid := user(t, db)
		_, err = db.Exec(`UPDATE users SET email=$2 WHERE id=$1`, uid, email)
		require.NoError(t, err)
		rankingUsage(t, db, uid, int64(100-i), fmt.Sprint(2-i), beijingDayStart(time.Now()))
	}
	recorder := mockHelpInteractions(t, s, false)
	for i, command := range []string{"/今日排行榜", "/今日排行版"} {
		id := fmt.Sprintf("ranking-query-%d", i)
		event := Event{ID: id, GroupID: "group", Content: command}
		event.Author.MemberID = "unbound-viewer"
		raw, err := json.Marshal(event)
		require.NoError(t, err)
		require.NoError(t, s.Enqueue(ctx, bot, Payload{ID: id, Type: groupMessageCreate, Data: raw}))
		s.runOne(ctx)
	}
	recorder.mu.Lock()
	require.Len(t, recorder.replies, 2)
	for _, reply := range recorder.replies {
		require.Contains(t, reply.text(), "**Token 消耗榜**")
		require.Contains(t, reply.text(), "**消费金额榜**")
		require.NotContains(t, reply.text(), bindFirst)
		require.Equal(t, 2, strings.Count(reply.text(), `1. 123\*\*\*\*89@qq.com`))
		require.Equal(t, 2, strings.Count(reply.text(), `2. sec\*\*\*\*on@example.com`))
		require.NotContains(t, reply.text(), "123456789")
		require.NotContains(t, reply.text(), "second_person")
	}
	recorder.mu.Unlock()
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_daily_usage`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&count))
	require.Zero(t, count)
}
