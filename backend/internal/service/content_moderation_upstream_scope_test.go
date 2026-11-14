package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type moderationScopeAccounts struct {
	AccountRepository
	accounts map[int64]*Account
}

func (r *moderationScopeAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	a := r.accounts[id]
	if a == nil {
		return nil, ErrAccountNotFound
	}
	return a, nil
}

func TestModerationUpstreamPersistenceAndValidation(t *testing.T) {
	ctx := context.Background()
	repo := &contentModerationTestSettingRepo{values: map[string]string{SettingKeyRiskControlEnabled: "true"}}
	accounts := &moderationScopeAccounts{accounts: map[int64]*Account{1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, GroupIDs: []int64{10, 20}}, 2: {ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, GroupIDs: []int64{20}}}}
	s := NewContentModerationService(repo, nil, nil, nil, nil, nil, nil, nil, accounts)
	legacy, err := s.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, ModerationUpstreamAll, legacy.UpstreamAccountScope)
	selected := ModerationUpstreamSelected
	ids := []int64{2, 1, 1}
	view, err := s.UpdateConfig(ctx, UpdateContentModerationConfigInput{UpstreamAccountScope: &selected, UpstreamAccountIDs: &ids})
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{1, 2}, view.UpstreamAccountIDs)
	fresh := NewContentModerationService(repo, nil, nil, nil, nil, nil, nil, nil, accounts)
	reloaded, err := fresh.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, view.UpstreamAccountIDs, reloaded.UpstreamAccountIDs)
	require.Equal(t, selected, reloaded.UpstreamAccountScope)
	for _, invalid := range []string{"unknown", "", "ALL"} {
		_, err = fresh.UpdateConfig(ctx, UpdateContentModerationConfigInput{UpstreamAccountScope: &invalid})
		require.Error(t, err)
	}
	for _, invalid := range [][]int64{{0}, {-1}, {999}} {
		_, err = fresh.UpdateConfig(ctx, UpdateContentModerationConfigInput{UpstreamAccountIDs: &invalid})
		require.Error(t, err)
	}
	accounts.accounts[3] = &Account{ID: 3, Platform: "customer", Type: AccountTypeAPIKey}
	invalid := []int64{3}
	_, err = fresh.UpdateConfig(ctx, UpdateContentModerationConfigInput{UpstreamAccountIDs: &invalid})
	require.Error(t, err)
	accounts.accounts[3] = &Account{ID: 3, Platform: PlatformOpenAI, Type: "bps"}
	_, err = fresh.UpdateConfig(ctx, UpdateContentModerationConfigInput{UpstreamAccountIDs: &invalid})
	require.Error(t, err)
	var stored ContentModerationConfig
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyContentModerationConfig]), &stored))
	group := int64(10)
	stored.AllGroups = false
	stored.GroupIDs = []int64{10, 20}
	input := ContentModerationCheckInput{GroupID: &group, Model: "gpt-5.1", UpstreamAccount: accounts.accounts[1]}
	applies, err := fresh.UpstreamModerationApplies(ctx, &stored, input)
	require.NoError(t, err)
	require.True(t, applies)
	group = 30
	applies, err = fresh.UpstreamModerationApplies(ctx, &stored, input)
	require.NoError(t, err)
	require.False(t, applies)
	group = 10
	accounts.accounts[1].GroupIDs = []int64{30}
	_, err = fresh.UpstreamModerationApplies(ctx, &stored, input)
	require.Error(t, err, "moving accounts cannot widen scope")
	_, err = fresh.UpdateConfig(ctx, UpdateContentModerationConfigInput{})
	require.Error(t, err, "unrelated saves cannot silently rebind a moved account")
	delete(accounts.accounts, 1)
	_, err = fresh.UpstreamModerationApplies(ctx, &stored, input)
	require.Error(t, err, "deleted account must fail closed")
	stored.UpstreamAccountIDs = nil
	applies, err = fresh.UpstreamModerationApplies(ctx, &stored, input)
	require.NoError(t, err)
	require.False(t, applies, "empty selected must not mean all")
	stored.UpstreamAccountScope = "future"
	_, err = fresh.UpstreamModerationApplies(ctx, &stored, input)
	require.Error(t, err)
}
