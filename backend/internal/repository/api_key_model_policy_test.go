package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyModelPolicyPersistenceAndUserUpdateIsolation(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	u := mustCreateAPIKeyRepoUser(t, ctx, client, "model-policy@test.invalid")
	key := &service.APIKey{UserID: u.ID, Key: "sk-model-policy-fixture", Name: "policy", Status: service.StatusActive}
	require.NoError(t, repo.Create(ctx, key))
	stale, err := repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.Empty(t, stale.ModelMapping)
	require.Empty(t, stale.ReasoningEffort)
	key.ModelMapping = map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}
	key.ReasoningEffort = "high"
	require.NoError(t, repo.Update(ctx, key, service.APIKeyUpdateFields{ModelMapping: true, ReasoningEffort: true}))

	// A user rename using a stale snapshot must preserve the administrator policy.
	stale.Name = "renamed"
	require.NoError(t, repo.Update(ctx, stale, service.APIKeyUpdateFields{Name: true}))
	for _, read := range []func() (*service.APIKey, error){
		func() (*service.APIKey, error) { return repo.GetByID(ctx, key.ID) },
		func() (*service.APIKey, error) { return repo.GetByKey(ctx, key.Key) },
		func() (*service.APIKey, error) { return repo.GetByKeyForAuth(ctx, key.Key) },
	} {
		got, err := read()
		require.NoError(t, err)
		require.Equal(t, key.ModelMapping, got.ModelMapping)
		require.Equal(t, "high", got.ReasoningEffort)
	}
	listed, _, err := repo.ListByUserID(ctx, u.ID, pagination.PaginationParams{Page: 1, PageSize: 10}, service.APIKeyListFilters{})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, key.ModelMapping, listed[0].ModelMapping)

	// Updating only effort also preserves mappings written since the caller read.
	stale.ReasoningEffort = "low"
	require.NoError(t, repo.Update(ctx, stale, service.APIKeyUpdateFields{ReasoningEffort: true}))
	got, err := repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, key.ModelMapping, got.ModelMapping)
	require.Equal(t, "low", got.ReasoningEffort)
	key.ModelMapping, key.ReasoningEffort = map[string]string{}, ""
	require.NoError(t, repo.Update(ctx, key, service.APIKeyUpdateFields{ModelMapping: true, ReasoningEffort: true}))
	got, err = repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.Empty(t, got.ModelMapping)
	require.Empty(t, got.ReasoningEffort)
	require.Equal(t, "renamed", got.Name)
}

func TestAPIKeyModelPolicyCombinedGroupFailureRollsBack(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	u := mustCreateAPIKeyRepoUser(t, ctx, client, "model-policy-rollback@test.invalid")
	group, err := client.Group.Create().SetName("policy-group").Save(ctx)
	require.NoError(t, err)
	key := &service.APIKey{UserID: u.ID, GroupID: &group.ID, Key: "sk-model-policy-rollback", Name: "policy", Status: service.StatusActive}
	require.NoError(t, repo.Create(ctx, key))
	key.ModelMapping, key.ReasoningEffort, key.Usage5h = map[string]string{"source": "original"}, "low", 8
	require.NoError(t, repo.Update(ctx, key, service.APIKeyUpdateFields{ModelMapping: true, ReasoningEffort: true, RateLimitUsage: true}))

	// Force the group-link insert to fail after the key row has been updated.
	// The transaction must also roll back its policy fields and usage reset.
	key.GroupIDs = []int64{group.ID, 999999999}
	key.ModelMapping, key.ReasoningEffort, key.Usage5h = map[string]string{"source": "changed"}, "high", 0
	err = repo.Update(ctx, key, service.APIKeyUpdateFields{GroupID: true, ModelMapping: true, ReasoningEffort: true, RateLimitUsage: true})
	require.Error(t, err)
	got, err := repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{group.ID}, got.GroupIDs)
	require.Equal(t, map[string]string{"source": "original"}, got.ModelMapping)
	require.Equal(t, "low", got.ReasoningEffort)
	require.Equal(t, 8.0, got.Usage5h)
}
