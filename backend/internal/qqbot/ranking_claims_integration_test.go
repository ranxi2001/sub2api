//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func seedPendingRankingReward(t *testing.T, db *sql.DB, uid int64, kind string, reward GrowthReward) string {
	t.Helper()
	key := fmt.Sprintf("qq-ranking:app:2026-10-20:%s:%d", kind, uid)
	raw, err := json.Marshal(reward)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO qq_bot_ranking_claims(app_id,ranking_day,ranking_kind,rank,user_id,source_key,reward) VALUES('app','2026-10-20',$1,1,$2,$3,$4::jsonb)`, kind, uid, key, string(raw))
	require.NoError(t, err)
	return key
}

func TestPendingRankingRewardsUseSnapshotsAndDeduplicateConcurrently(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	// There is no binding or enabled reward configuration. The earned snapshot suffices.
	seedPendingRankingReward(t, db, uid, "tokens", GrowthReward{Type: "balance", Amount: 1.00000001})
	seedPendingRankingReward(t, db, uid, "amount", GrowthReward{Type: "physical", Quantity: 1, PrizeName: "Original cup"})
	_, err = s.SaveRankingConfig(ctx, defaultRankingConfig())
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.settlePendingRankingRewards(ctx, c, time.Now()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 1.00000001, balance)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_ranking_claims WHERE claimed_at IS NOT NULL`).Scan(&count))
	require.Equal(t, 2, count)
	page, err := s.ListRewardCodes(ctx, "", "pending", 1)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "Original cup", page.Items[0].PrizeName)
	require.NoError(t, s.UpdateRewardCode(ctx, page.Items[0].ID, "fulfill", "Delivered"))
	bind(t, db, uid, "member")
	text, err := s.growthTasks(ctx, c, Job{MemberID: "member", GroupID: "group"})
	require.NoError(t, err)
	require.Contains(t, text, "余额 USD 1.00000001")
	require.NotContains(t, text, "可领取")
	require.NotContains(t, text, "/福利")
	other := user(t, db)
	bind(t, db, other, "other")
	text, err = s.growthTasks(ctx, c, Job{MemberID: "other", GroupID: "group"})
	require.NoError(t, err)
	require.NotContains(t, text, "Original cup")
}

func TestPendingRankingRewardsRollbackAndSkipUnavailableAccounts(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid, disabled, deleted := user(t, db), user(t, db), user(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	for _, id := range []int64{uid, disabled, deleted} {
		seedPendingRankingReward(t, db, id, "tokens", GrowthReward{Type: "balance", Amount: 2})
	}
	_, err = db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, disabled)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE users SET deleted_at=NOW() WHERE id=$1`, deleted)
	require.NoError(t, err)
	wrong := c
	wrong.AppID = "other"
	require.NoError(t, s.settlePendingRankingRewards(ctx, wrong, time.Now()))
	_, err = db.Exec(`ALTER TABLE redeem_codes ADD CONSTRAINT reject_ranking CHECK(value<>2)`)
	require.NoError(t, err)
	require.Error(t, s.settlePendingRankingRewards(ctx, c, time.Now()))
	for _, table := range []string{"qq_bot_rewards", "qq_bot_reward_codes", "redeem_codes"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
		require.Zero(t, count)
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_ranking_claims WHERE claimed_at IS NOT NULL`).Scan(&count))
	require.Zero(t, count)
	var total float64
	require.NoError(t, db.QueryRow(`SELECT SUM(balance) FROM users`).Scan(&total))
	require.Zero(t, total)
	_, err = db.Exec(`ALTER TABLE redeem_codes DROP CONSTRAINT reject_ranking`)
	require.NoError(t, err)
	require.NoError(t, s.settlePendingRankingRewards(ctx, c, time.Now()))
	require.NoError(t, db.QueryRow(`SELECT SUM(balance) FROM users`).Scan(&total))
	require.Equal(t, 2., total)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_ranking_claims WHERE claimed_at IS NULL`).Scan(&count))
	require.Equal(t, 2, count)
}

func TestPendingRankingRewardsPreserveLegacyGrantsAndDrainBatches(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	for i := range 103 {
		uid := user(t, db)
		key := seedPendingRankingReward(t, db, uid, "tokens", GrowthReward{Type: "chances", Quantity: 2})
		if i == 0 {
			_, err = db.Exec(`INSERT INTO qq_bot_lottery_chance_grants(source_key,user_id,chances) VALUES($1,$2,2)`, key, uid)
			require.NoError(t, err)
		}
	}
	require.NoError(t, s.settlePendingRankingRewards(ctx, c, time.Now()))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_ranking_claims WHERE claimed_at IS NULL`).Scan(&count))
	require.Equal(t, 3, count)
	require.NoError(t, s.settlePendingRankingRewards(ctx, c, time.Now()))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_ranking_claims WHERE claimed_at IS NULL`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants`).Scan(&count))
	require.Equal(t, 206, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&count))
	require.Equal(t, 102, count)
}
