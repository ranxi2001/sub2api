//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type modelPolicyAPIKeyRepoStub struct {
	apiKeyRepoStubForGroupUpdate
	fields APIKeyUpdateFields
}

func TestAdminAPIKeyModelPolicyAndUsageResetShareWriteMask(t *testing.T) {
	now := time.Now()
	repo := &modelPolicyAPIKeyRepoStub{apiKeyRepoStubForGroupUpdate: apiKeyRepoStubForGroupUpdate{
		key: &APIKey{ID: 1, Key: "sk-policy-fixture", Usage5h: 8, Usage1d: 9, Usage7d: 10, Window5hStart: &now, Window1dStart: &now, Window7dStart: &now},
	}}
	effort := "high"
	svc := &adminServiceImpl{apiKeyRepo: repo}
	result, err := svc.AdminUpdateAPIKeySettings(context.Background(), 1, AdminUpdateAPIKeySettingsInput{ReasoningEffort: &effort, ResetRateLimitUsage: true})
	require.NoError(t, err)
	require.Equal(t, APIKeyUpdateFields{ReasoningEffort: true, RateLimitUsage: true}, repo.fields)
	require.Equal(t, "high", result.APIKey.ReasoningEffort)
	require.Zero(t, result.APIKey.Usage5h)
	require.Zero(t, result.APIKey.Usage1d)
	require.Zero(t, result.APIKey.Usage7d)
	require.Nil(t, result.APIKey.Window5hStart)
	require.Nil(t, result.APIKey.Window1dStart)
	require.Nil(t, result.APIKey.Window7dStart)
}

func (r *modelPolicyAPIKeyRepoStub) Update(ctx context.Context, key *APIKey, fields APIKeyUpdateFields) error {
	r.fields = fields
	return r.apiKeyRepoStubForGroupUpdate.Update(ctx, key, fields)
}

func TestAdminAPIKeyModelPolicyValidation(t *testing.T) {
	for _, effort := range []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"} {
		require.NoError(t, (AdminUpdateAPIKeySettingsInput{ReasoningEffort: &effort}).Validate())
	}
	for _, effort := range []string{"auto", "HIGH", " high", "invalid"} {
		require.Error(t, (AdminUpdateAPIKeySettingsInput{ReasoningEffort: &effort}).Validate())
	}
	for _, model := range []string{"", "gpt 6", " gpt-6", "gpt-6 ", "gpt\t6", "gpt\x006", strings.Repeat("a", 201)} {
		for _, mapping := range []map[string]string{{model: "target"}, {"source": model}} {
			require.Error(t, (AdminUpdateAPIKeySettingsInput{ModelMapping: &mapping}).Validate())
		}
	}
	mapping := map[string]string{"gpt-6.1-sol": "gpt-5.6-sol", "gpt-5.6-sol": "gpt-6.1-sol"}
	// Chains/cycles are harmless because forwarding applies exactly one lookup.
	require.NoError(t, (AdminUpdateAPIKeySettingsInput{ModelMapping: &mapping}).Validate())
	for i := 0; i < 101; i++ {
		mapping[fmt.Sprintf("source-%d", i)] = "target"
	}
	require.Error(t, (AdminUpdateAPIKeySettingsInput{ModelMapping: &mapping}).Validate())
}

func TestAdminAPIKeyModelPolicyUpdatePreservesUnrelatedFieldsAndInvalidatesOnlyKey(t *testing.T) {
	groupID := int64(9)
	existing := &APIKey{ID: 1, Key: "sk-policy-fixture", Name: "original", GroupID: &groupID, GroupIDs: []int64{9, 10}, ReasoningEffort: "low"}
	repo := &modelPolicyAPIKeyRepoStub{apiKeyRepoStubForGroupUpdate: apiKeyRepoStubForGroupUpdate{key: existing}}
	cache := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{apiKeyRepo: repo, authCacheInvalidator: cache}
	mapping := map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}
	result, err := svc.AdminUpdateAPIKeySettings(context.Background(), 1, AdminUpdateAPIKeySettingsInput{ModelMapping: &mapping})
	require.NoError(t, err)
	require.Equal(t, APIKeyUpdateFields{ModelMapping: true}, repo.fields)
	require.Equal(t, "low", result.APIKey.ReasoningEffort)
	require.Equal(t, existing.GroupIDs, result.APIKey.GroupIDs)
	require.Equal(t, []string{existing.Key}, cache.keys)
	require.Empty(t, cache.userIDs)
	require.Empty(t, cache.groupIDs)
	mapping["gpt-6.1-sol"] = "changed"
	require.Equal(t, "gpt-5.6-sol", result.APIKey.ModelMapping["gpt-6.1-sol"])
	require.Empty(t, existing.ModelMapping)

	emptyMapping, emptyEffort := map[string]string{}, ""
	_, err = svc.AdminUpdateAPIKeySettings(context.Background(), 1, AdminUpdateAPIKeySettingsInput{ModelMapping: &emptyMapping, ReasoningEffort: &emptyEffort})
	require.NoError(t, err)
	require.Empty(t, repo.updated.ModelMapping)
	require.Empty(t, repo.updated.ReasoningEffort)
	require.Equal(t, APIKeyUpdateFields{ModelMapping: true, ReasoningEffort: true}, repo.fields)
}

func TestAdminAPIKeyModelPolicyFailureDoesNotInvalidateCache(t *testing.T) {
	repo := &modelPolicyAPIKeyRepoStub{apiKeyRepoStubForGroupUpdate: apiKeyRepoStubForGroupUpdate{
		key: &APIKey{ID: 1, Key: "sk-policy-fixture"}, updateErr: errors.New("write failed"),
	}}
	cache := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{apiKeyRepo: repo, authCacheInvalidator: cache}
	effort := "high"
	_, err := svc.AdminUpdateAPIKeySettings(context.Background(), 1, AdminUpdateAPIKeySettingsInput{ReasoningEffort: &effort})
	require.Error(t, err)
	require.Empty(t, cache.keys)
	effort = "invalid"
	repo.fields = APIKeyUpdateFields{}
	_, err = svc.AdminUpdateAPIKeySettings(context.Background(), 1, AdminUpdateAPIKeySettingsInput{ReasoningEffort: &effort})
	require.Error(t, err)
	require.True(t, repo.fields.IsEmpty())
}
