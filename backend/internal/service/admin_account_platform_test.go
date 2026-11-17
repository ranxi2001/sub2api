//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func newPlatformEditService() (*adminServiceImpl, *accountRepoStubForBulkUpdate) {
	repo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{
		7: {ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
			Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://fixture.invalid", "model_mapping": map[string]any{"old-model": "old-target"}, "refresh_token": "old-oauth-residue"},
			Extra:       map[string]any{"quota_used": 12.0, "quota_daily": 25.0, "cost_multiplier": 0.5, "openai_passthrough": true, "grok_observed_models": []any{"old-model"}}},
	}}
	groups := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{
		1: {ID: 1, Platform: PlatformOpenAI},
		2: {ID: 2, Platform: PlatformAnthropic},
		3: {ID: 3, Platform: PlatformComposite},
	}}
	return &adminServiceImpl{accountRepo: repo, groupRepo: groups}, repo
}

func TestUpdateAccountPlatformRetainsKeyAndCompatibleGroups(t *testing.T) {
	svc, repo := newPlatformEditService()
	source := repo.getByIDAccounts[7]
	source.GroupIDs = []int64{1, 2, 3}
	_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Platform: PlatformAnthropic, SkipMixedChannelCheck: true})
	require.NoError(t, err)
	require.Len(t, repo.updatedAccounts, 1)
	updated := repo.updatedAccounts[0]
	require.Equal(t, PlatformAnthropic, updated.Platform)
	require.Equal(t, map[string]any{"api_key": "fixture-key", "base_url": "https://fixture.invalid"}, updated.Credentials)
	require.Equal(t, map[string]any{"quota_used": 12.0, "quota_daily": 25.0, "cost_multiplier": 0.5}, updated.Extra)
	require.Equal(t, []int64{2, 3}, repo.bindGroupsByAccount[7])
	require.Equal(t, PlatformOpenAI, source.Platform, "draft changes must not mutate the source snapshot")
}

func TestUpdateAccountPlatformOmittedKeepsExistingSettings(t *testing.T) {
	svc, repo := newPlatformEditService()
	_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Name: "Renamed fixture"})
	require.NoError(t, err)
	require.Equal(t, PlatformOpenAI, repo.updatedAccounts[0].Platform)
	require.Contains(t, repo.updatedAccounts[0].Credentials, "model_mapping")
	require.Contains(t, repo.updatedAccounts[0].Extra, "openai_passthrough")
	require.Empty(t, repo.bindGroupsCalls)
}

func TestUpdateAccountPlatformRejectsInvalidTargetsBeforeWriting(t *testing.T) {
	for _, target := range []string{"unknown", PlatformComposite, PlatformAntigravity} {
		t.Run(target, func(t *testing.T) {
			svc, repo := newPlatformEditService()
			_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Platform: target})
			require.Error(t, err)
			require.Empty(t, repo.updatedAccounts)
			require.Empty(t, repo.bindGroupsCalls)
		})
	}
}

func TestUpdateAccountPlatformRejectsBoundAuthorizationAndTypeConversion(t *testing.T) {
	for _, credentialType := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeBedrock, AccountTypeUpstream} {
		t.Run(credentialType, func(t *testing.T) {
			svc, repo := newPlatformEditService()
			repo.getByIDAccounts[7].Type = credentialType
			_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Platform: PlatformAnthropic})
			require.Error(t, err)
			require.Empty(t, repo.updatedAccounts)
		})
	}
	svc, repo := newPlatformEditService()
	_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Platform: PlatformAnthropic, Type: AccountTypeOAuth})
	require.Error(t, err)
	require.Empty(t, repo.updatedAccounts)
}

func TestUpdateAccountPlatformRejectsIncompatibleExplicitGroupBeforeWriting(t *testing.T) {
	svc, repo := newPlatformEditService()
	groups := []int64{1}
	_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Platform: PlatformAnthropic, GroupIDs: &groups, SkipMixedChannelCheck: true})
	require.Error(t, err)
	require.Empty(t, repo.updatedAccounts)
	require.Empty(t, repo.bindGroupsCalls)
}

func TestUpdateAccountPlatformRetainsGoogleServiceAccountCredentials(t *testing.T) {
	svc, repo := newPlatformEditService()
	account := repo.getByIDAccounts[7]
	account.Platform, account.Type = PlatformGemini, AccountTypeServiceAccount
	account.Credentials = map[string]any{"service_account_json": "fixture-json", "project_id": "fixture-project", "client_email": "fixture@example.invalid", "location": "global", "model_mapping": map[string]any{"old": "old"}}
	_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Platform: PlatformAnthropic})
	require.NoError(t, err)
	updated := repo.updatedAccounts[0]
	require.Equal(t, PlatformAnthropic, updated.Platform)
	require.Equal(t, "fixture-json", updated.Credentials["service_account_json"])
	require.NotContains(t, updated.Credentials, "model_mapping")
}

func TestUpdateAccountPlatformRejectsShadowBeforeWriting(t *testing.T) {
	svc, repo := newPlatformEditService()
	parent := int64(6)
	repo.getByIDAccounts[7].ParentAccountID = &parent
	_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Platform: PlatformAnthropic})
	require.Error(t, err)
	require.Empty(t, repo.updatedAccounts)
}

type platformParentRepo struct{ *accountRepoStubForBulkUpdate }

func (r *platformParentRepo) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	return []*Account{{ID: 8, Platform: PlatformOpenAI}}, nil
}

func TestUpdateAccountPlatformRejectsParentWithDependentShadows(t *testing.T) {
	svc, repo := newPlatformEditService()
	svc.accountRepo = &platformParentRepo{repo}
	_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Platform: PlatformAnthropic})
	require.Error(t, err)
	require.Empty(t, repo.updatedAccounts)
}
