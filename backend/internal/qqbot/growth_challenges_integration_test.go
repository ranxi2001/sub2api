//go:build integration

package qqbot

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGrowthChallengeCalendarSettlementAndConcurrency(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid, other := user(t, db), user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{
		{ID: "daily", Name: "Daily explorer", Kind: "model_count", Period: "daily", Target: 2, Chances: 1, Enabled: true},
		{ID: "weekly", Name: "Weekly activity", Kind: "usage_days", Period: "weekly", Target: 2, Chances: 2, Enabled: true},
		{ID: "lifetime", Name: "Model collection", Kind: "model_count", Target: 4, Chances: 3, Enabled: true},
	}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	at := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,created_at) VALUES
 ($1,'model-a',$3),($1,'model-a',$3),($1,'model-b',$3),
 ($1,'model-c',$3-INTERVAL '1 day'),($1,'model-d',$3-INTERVAL '10 days'),
 ($1,'future',$3+INTERVAL '1 day'),($2,'other',$3)`, uid, other, at)
	require.NoError(t, err)
	j := Job{GroupID: "group", MemberID: "member"}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.processGrowthTasks(ctx, c, j, at, false)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var chances int
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, uid).Scan(&chances))
	require.Equal(t, 6, chances)
	text, n, err := s.processGrowthTasks(ctx, c, j, at, false)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Contains(t, text, "每日挑战")
	require.Contains(t, text, "每周挑战")
	require.NotContains(t, text, "连续签到")
	// The next Beijing day is also a Monday. Old usage cannot satisfy either
	// reset challenge, while the permanent achievement remains claimed.
	monday := at.AddDate(0, 0, 1)
	text, n, err = s.processGrowthTasks(ctx, c, j, monday, false)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Contains(t, text, "1/2 个模型")
	require.Contains(t, text, "1/2 天")
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,created_at) VALUES($1,'monday-second',$2),($1,'tuesday',$2+INTERVAL '1 day')`, uid, monday)
	require.NoError(t, err)
	_, n, err = s.processGrowthTasks(ctx, c, j, monday, false)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, n, err = s.processGrowthTasks(ctx, c, j, monday.AddDate(0, 0, 1), false)
	require.NoError(t, err)
	require.Equal(t, 1, n, "weekly progress spans Beijing days; Monday daily reward is not paid again")
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, uid).Scan(&chances))
	require.Equal(t, 9, chances)
}

func TestGrowthChallengeInvitesUseSettledDistinctAccountsAndWindow(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	uid, first, second, other := user(t, db), user(t, db), user(t, db), user(t, db)
	at := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	for i, source := range []int64{first, first, second, uid} {
		_, err := db.Exec(`INSERT INTO qq_bot_rewards(reward_key,app_id,group_id,member_id,user_id,kind,source_user_id,amount,created_at) VALUES($1,'app','group','member',$2,'invite',$3,1,$4)`, fmt.Sprintf("invite-test-%d", i), uid, source, at.Add(-time.Duration(i)*24*time.Hour))
		require.NoError(t, err)
	}
	_, err := db.Exec(`INSERT INTO qq_bot_rewards(reward_key,app_id,group_id,member_id,user_id,kind,source_user_id,amount,created_at) VALUES('future','app','group','member',$1,'invite',$2,1,$3),('other','app','group','member',$4,'invite',$2,1,$5)`, uid, other, at.AddDate(0, 0, 1), other, at)
	require.NoError(t, err)
	progress, err := loadGrowthProgress(ctx, db, uid, []GrowthTask{
		{Kind: "invite_count"}, {Kind: "invite_count", Period: "daily"}, {Kind: "invite_count", Period: "weekly"},
	}, at, false)
	require.NoError(t, err)
	require.Equal(t, 2, progress["once"].invites)
	require.Equal(t, 1, progress["daily"].invites)
	require.Equal(t, 2, progress["weekly"].invites)
}

func TestGrowthChallengeMonitorFindsNextCalendarPeriod(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{
		{ID: "daily_api", Name: "Daily API", Kind: "usage_count", Period: "daily", Target: 1, Chances: 2, Enabled: true},
	}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	at := time.Now().Add(-time.Second)
	yesterday := at.AddDate(0, 0, -1)
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,created_at) VALUES($1,'yesterday',$2),($1,'today',$3)`, uid, yesterday, at)
	require.NoError(t, err)
	_, n, err := s.processGrowthTasks(ctx, c, Job{GroupID: "group", MemberID: "member"}, yesterday, false)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, err = s.settleGrowthBatch(ctx, c, 0)
	require.NoError(t, err)
	var chances int
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, uid).Scan(&chances))
	require.Equal(t, 4, chances, "yesterday's claim cannot exclude today's candidate")
	_, err = s.settleGrowthBatch(ctx, c, 0)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, uid).Scan(&chances))
	require.Equal(t, 4, chances)
}

func TestGrowthChallengeSettlesPreviousDayAndWeekAcrossMidnight(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{
		{ID: "day", Name: "Daily", Kind: "usage_count", Period: "daily", Target: 1, Chances: 1, Enabled: true},
		{ID: "week", Name: "Weekly", Kind: "usage_days", Period: "weekly", Target: 1, Chances: 2, Enabled: true},
	}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	// Sunday 23:59:50 Beijing: the next monitor pass is Monday 00:00:10.
	lastRequest := time.Date(2026, 11, 8, 15, 59, 50, 0, time.UTC)
	afterReset := lastRequest.Add(20 * time.Second)
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,created_at) VALUES($1,'last-request',$2)`, uid, lastRequest)
	require.NoError(t, err)
	j := Job{GroupID: "group", MemberID: "member"}
	text, n, err := s.processGrowthTasks(ctx, c, j, afterReset, false)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Contains(t, text, "0/1 次", "current day progress must remain separate from the catch-up")
	require.Contains(t, text, "0/1 天")
	var chances int
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, uid).Scan(&chances))
	require.Equal(t, 3, chances)
	_, n, err = s.processGrowthTasks(ctx, c, j, afterReset, false)
	require.NoError(t, err)
	require.Zero(t, n)
	// A request exactly at the new midnight must never satisfy the old period,
	// even through PostgreSQL's microsecond timestamp conversion.
	other := user(t, db)
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,created_at) VALUES($1,'new-midnight',$2)`, other, growthPeriodStart("daily", afterReset))
	require.NoError(t, err)
	progress, err := loadGrowthProgress(ctx, db, other, cfg.Growth.Tasks, growthPreviousPeriodEnd("daily", afterReset), false)
	require.NoError(t, err)
	require.Zero(t, progress["daily"].usage)
	require.Zero(t, progress["weekly"].days)
}
