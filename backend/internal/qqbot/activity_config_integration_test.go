//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActivityAndBasicConfigSavesPreserveEachOther(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	original, err := s.Config(ctx)
	require.NoError(t, err)
	// An already-open basic form contains invitation settings from before the activity edit.
	staleBasic := original.Public()
	invite := InviteConfig{Enabled: true, InviteReward: 2.5, InviteDailyLimit: 8,
		RechargeRequirement: RechargeRequirement{MinRechargeTotal: 20, MinRecharge30d: 5, RechargeWindowDays: 7}}
	require.NoError(t, s.SaveInviteConfig(ctx, invite))
	staleBasic.ChatModel = "updated-model"
	_, err = s.SaveBasicConfig(ctx, staleBasic)
	require.NoError(t, err)
	gotInvite, err := s.InviteConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, invite, gotInvite)
	invite.InviteReward = .12345678
	require.NoError(t, s.SaveInviteConfig(ctx, invite))
	got, err := s.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, "updated-model", got.ChatModel)
	require.Equal(t, invite.InviteReward, got.InviteReward)
	require.Equal(t, invite.MinRechargeTotal, got.InviteMinRechargeTotal)
	require.Equal(t, invite.MinRecharge30d, got.InviteMinRecharge30d)
	require.Equal(t, invite.RechargeWindowDays, got.InviteRechargeWindowDays)
	require.Equal(t, original.AppSecret, got.AppSecret)
	require.Equal(t, original.APIKey, got.APIKey)
	require.Equal(t, original.AllowedGroups, got.AllowedGroups)
	require.Equal(t, original.CheckinReward, got.CheckinReward)

	// Both writers must merge after acquiring the shared lock, regardless of save order.
	for i := 1; i <= 5; i++ {
		basic := got.Public()
		basic.ChatModel = fmt.Sprintf("concurrent-model-%d", i)
		nextInvite := InviteConfig{Enabled: i%2 == 0, InviteReward: float64(i), InviteDailyLimit: i,
			RechargeRequirement: RechargeRequirement{MinRechargeTotal: float64(i), MinRecharge30d: float64(i) + .5, RechargeWindowDays: i}}
		errors := make(chan error, 2)
		go func() { _, err := s.SaveBasicConfig(ctx, basic); errors <- err }()
		go func() { errors <- s.SaveInviteConfig(ctx, nextInvite) }()
		require.NoError(t, <-errors)
		require.NoError(t, <-errors)
		got, err = s.Config(ctx)
		require.NoError(t, err)
		require.Equal(t, basic.ChatModel, got.ChatModel)
		require.Equal(t, nextInvite.Enabled, got.InviteEnabled)
		require.Equal(t, nextInvite.InviteReward, got.InviteReward)
		require.Equal(t, nextInvite.InviteDailyLimit, got.InviteDailyLimit)
		require.Equal(t, nextInvite.MinRechargeTotal, got.InviteMinRechargeTotal)
		require.Equal(t, nextInvite.MinRecharge30d, got.InviteMinRecharge30d)
		require.Equal(t, nextInvite.RechargeWindowDays, got.InviteRechargeWindowDays)
		require.Equal(t, original.AppSecret, got.AppSecret)
		require.Equal(t, original.APIKey, got.APIKey)
	}
}

func TestCheckinDistributionPersistsAndLegacySavesPreserveIt(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	_, err := db.Exec(`INSERT INTO settings(key,value) VALUES($1,$2)`, checkinConfigKey, `{"enabled":true,"min_amount":1,"max_amount":3}`)
	require.NoError(t, err)
	original, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, &CheckinRewardDistribution{7000, 2000, 1000}, original.RewardDistribution)
	custom := original
	custom.RewardDistribution = &CheckinRewardDistribution{1234, 4321, 4445}
	require.NoError(t, s.SaveCheckinConfig(WithCheckinSection(WithConfigVersion(ctx, CheckinSectionVersion(original, "checkin")), "checkin"), custom))
	restarted := New(db, testEncryptor{}, true, nil)
	saved, err := restarted.CheckinConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, custom.RewardDistribution, saved.RewardDistribution)
	require.NotEqual(t, CheckinSectionVersion(original, "checkin"), CheckinSectionVersion(saved, "checkin"))
	require.Equal(t, CheckinSectionVersion(original, "tasks"), CheckinSectionVersion(saved, "tasks"))
	require.ErrorIs(t, s.SaveCheckinConfig(WithConfigVersion(ctx, ConfigVersion(original)), original), ErrConfigConflict)
	// Missing and null fields from older clients preserve saved probabilities.
	for _, raw := range []string{`{"enabled":true,"min_amount":2,"max_amount":4}`, `{"enabled":true,"min_amount":2,"max_amount":4,"reward_distribution":null}`} {
		var legacy CheckinConfig
		require.NoError(t, json.Unmarshal([]byte(raw), &legacy))
		require.NoError(t, s.SaveCheckinConfig(WithConfigVersion(ctx, ConfigVersion(saved)), legacy))
		saved, err = restarted.CheckinConfig(ctx)
		require.NoError(t, err)
		require.Equal(t, custom.RewardDistribution, saved.RewardDistribution)
	}
	invalid := saved
	invalid.RewardDistribution = &CheckinRewardDistribution{7000, 2000, 2000}
	require.Error(t, s.SaveCheckinConfig(ctx, invalid))
	var raw string
	require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key=$1`, checkinConfigKey).Scan(&raw))
	var stored CheckinConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	require.Equal(t, custom.RewardDistribution, stored.RewardDistribution)
}

func TestCheckinPayoutUsesConfiguredBandWithoutExposingProbabilities(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	bot, err := s.Config(ctx)
	require.NoError(t, err)
	for i, tc := range []struct {
		distribution CheckinRewardDistribution
		lo, hi       float64
	}{
		{CheckinRewardDistribution{10000, 0, 0}, 1, 1.99999999},
		{CheckinRewardDistribution{0, 10000, 0}, 2, 2.49999999},
		{CheckinRewardDistribution{0, 0, 10000}, 2.5, 3},
	} {
		cfg := CheckinConfig{Enabled: true, MinAmount: 1, MaxAmount: 3, RewardDistribution: &tc.distribution}
		require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
		uid := user(t, db)
		member := fmt.Sprintf("band-%d", i)
		bind(t, db, uid, member)
		reply, err := s.reward(ctx, bot, Job{MemberID: member, GroupID: "group"}, "checkin")
		require.NoError(t, err)
		require.Contains(t, reply, "签到成功")
		require.NotContains(t, reply, "概率")
		require.NotContains(t, reply, "reward_distribution")
		var balance float64
		require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
		require.GreaterOrEqual(t, balance, tc.lo)
		require.LessOrEqual(t, balance, tc.hi)
		public := cfg
		public.RewardDistribution = nil
		require.Equal(t, siteCheckinRules(public), siteCheckinRules(cfg))
	}
}
