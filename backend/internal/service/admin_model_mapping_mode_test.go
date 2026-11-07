//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBulkAliasScopeRepairPreservesExistingMappings(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.4": "gpt-5.6-sol"}, "access_token": "test-only"}}
	repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{account}}
	svc := &adminServiceImpl{accountRepo: repo}
	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Credentials: map[string]any{OpenAIModelMappingModeKey: "aliases"}})
	require.NoError(t, err)
	require.Equal(t, map[string]any{OpenAIModelMappingModeKey: "aliases"}, repo.lastBulkUpdate.Credentials)
	require.NotContains(t, repo.lastBulkUpdate.Credentials, "model_mapping")
	require.NotContains(t, repo.lastBulkUpdate.Extra, "openai_excel_bps")
	account.Credentials[OpenAIModelMappingModeKey] = "aliases"
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-sol": "gpt-6-sol"}}})
	require.NoError(t, err)
	require.Equal(t, "whitelist", repo.lastBulkUpdate.Credentials[OpenAIModelMappingModeKey], "legacy bulk model edits must restore an explicit allowlist")
}

func TestAccountModelScopeValidationBeforePersistence(t *testing.T) {
	repo := &updateAccountCredsRepoStub{account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "test-only"}}}
	svc := &adminServiceImpl{accountRepo: repo}
	_, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Credentials: map[string]any{OpenAIModelMappingModeKey: "invalid"}})
	require.Error(t, err)
	require.Zero(t, repo.updateCalls)
}

func TestOAuthInitialSelectionBulkOwnershipKeepsLegacyCredentials(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		for _, field := range []string{"model_mapping", OpenAIModelMappingModeKey, "compact_model_mapping"} {
			credentials := map[string]any{"model_mapping": map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"}}
			if snapshot {
				credentials[OAuthInitialModelSelectionKey] = []string{"gpt-6.1-sol"}
			}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: credentials}
			repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{account}}
			svc := &adminServiceImpl{accountRepo: repo}
			var value any = map[string]any{"gpt-6-luna": "gpt-6-luna"}
			if field == OpenAIModelMappingModeKey {
				value = "aliases"
			}
			_, err := svc.BulkUpdateAccounts(t.Context(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Credentials: map[string]any{field: value}})
			require.NoError(t, err)
			marker, exists := repo.lastBulkUpdate.Credentials[OAuthInitialModelSelectionKey]
			require.Equal(t, snapshot, exists, "legacy account updates must not gain a snapshot field")
			require.Nil(t, marker, "manual model changes release an existing scope")
		}
	}
}
