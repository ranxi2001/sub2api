//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminAPIKeyLongContextSettingPreservesOmittedAndInvalidatesSelectedKey(t *testing.T) {
	repo := &modelPolicyAPIKeyRepoStub{apiKeyRepoStubForGroupUpdate: apiKeyRepoStubForGroupUpdate{
		key: &APIKey{ID: 1, Key: "sk-long-context-settings", AllowLongContext: true, ReasoningEffort: "high", ModelMapping: map[string]string{"source": "target"}},
	}}
	cache := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{apiKeyRepo: repo, authCacheInvalidator: cache}
	result, err := svc.AdminUpdateAPIKeySettings(context.Background(), 1, AdminUpdateAPIKeySettingsInput{})
	require.NoError(t, err)
	require.True(t, result.APIKey.AllowLongContext)
	require.True(t, repo.fields.IsEmpty())
	require.Empty(t, cache.keys)
	for _, enabled := range []bool{false, true} {
		result, err = svc.AdminUpdateAPIKeySettings(context.Background(), 1, AdminUpdateAPIKeySettingsInput{AllowLongContext: &enabled})
		require.NoError(t, err)
		require.Equal(t, enabled, result.APIKey.AllowLongContext)
		require.Equal(t, APIKeyUpdateFields{AllowLongContext: true}, repo.fields)
		require.Equal(t, "high", result.APIKey.ReasoningEffort)
		require.Equal(t, map[string]string{"source": "target"}, result.APIKey.ModelMapping)
		repo.key = result.APIKey
		repo.fields = APIKeyUpdateFields{}
		preserved, err := svc.AdminUpdateAPIKeySettings(context.Background(), 1, AdminUpdateAPIKeySettingsInput{})
		require.NoError(t, err)
		require.Equal(t, enabled, preserved.APIKey.AllowLongContext)
		require.True(t, repo.fields.IsEmpty())
	}
	require.Equal(t, []string{"sk-long-context-settings", "sk-long-context-settings"}, cache.keys)
	require.Empty(t, cache.userIDs)
	require.Empty(t, cache.groupIDs)
}

func TestAdminAPIKeyLongContextFailureDoesNotInvalidateOrResetUsage(t *testing.T) {
	repo := &modelPolicyAPIKeyRepoStub{apiKeyRepoStubForGroupUpdate: apiKeyRepoStubForGroupUpdate{
		key: &APIKey{ID: 1, Key: "sk-long-context-failed", AllowLongContext: true, Usage5h: 8}, updateErr: errors.New("write failed"),
	}}
	cache := &authCacheInvalidatorStub{}
	enabled := false
	svc := &adminServiceImpl{apiKeyRepo: repo, authCacheInvalidator: cache}
	_, err := svc.AdminUpdateAPIKeySettings(context.Background(), 1, AdminUpdateAPIKeySettingsInput{AllowLongContext: &enabled, ResetRateLimitUsage: true})
	require.Error(t, err)
	require.Equal(t, APIKeyUpdateFields{AllowLongContext: true, RateLimitUsage: true}, repo.fields)
	require.True(t, repo.key.AllowLongContext)
	require.Equal(t, 8.0, repo.key.Usage5h)
	require.Empty(t, cache.keys)
}
