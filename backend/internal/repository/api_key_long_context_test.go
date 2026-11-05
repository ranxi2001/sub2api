package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyLongContextPersistenceAndWriteIsolation(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	u := mustCreateAPIKeyRepoUser(t, ctx, client, "long-context@test.invalid")
	key := &service.APIKey{UserID: u.ID, Key: "sk-long-context-fixture", Name: "context", Status: service.StatusActive}
	require.NoError(t, repo.Create(ctx, key))
	require.True(t, key.AllowLongContext, "repository creation must return the database default")
	stale, err := repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.True(t, stale.AllowLongContext)
	key.AllowLongContext = false
	require.NoError(t, repo.Update(ctx, key, service.APIKeyUpdateFields{AllowLongContext: true}))
	// A user's ordinary update using the old allowed snapshot cannot re-enable it.
	stale.Name = "renamed"
	require.NoError(t, repo.Update(ctx, stale, service.APIKeyUpdateFields{Name: true}))
	for _, read := range []func() (*service.APIKey, error){
		func() (*service.APIKey, error) { return repo.GetByID(ctx, key.ID) },
		func() (*service.APIKey, error) { return repo.GetByKey(ctx, key.Key) },
		func() (*service.APIKey, error) { return repo.GetByKeyForAuth(ctx, key.Key) },
	} {
		got, err := read()
		require.NoError(t, err)
		require.False(t, got.AllowLongContext)
		require.Equal(t, "renamed", got.Name)
	}
	key.AllowLongContext = true
	require.NoError(t, repo.Update(ctx, key, service.APIKeyUpdateFields{AllowLongContext: true}))
	got, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.True(t, got.AllowLongContext)
}

func TestAPIKeyLongContextCombinedGroupFailureRollsBack(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	u := mustCreateAPIKeyRepoUser(t, ctx, client, "long-context-rollback@test.invalid")
	group, err := client.Group.Create().SetName("long-context-group").Save(ctx)
	require.NoError(t, err)
	key := &service.APIKey{UserID: u.ID, GroupID: &group.ID, Key: "sk-long-context-rollback", Name: "context", Status: service.StatusActive}
	require.NoError(t, repo.Create(ctx, key))
	key.Usage5h = 8
	require.NoError(t, repo.Update(ctx, key, service.APIKeyUpdateFields{RateLimitUsage: true}))
	key.GroupIDs, key.AllowLongContext, key.Usage5h = []int64{group.ID, 999999999}, false, 0
	err = repo.Update(ctx, key, service.APIKeyUpdateFields{GroupID: true, AllowLongContext: true, RateLimitUsage: true})
	require.Error(t, err)
	got, err := repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.True(t, got.AllowLongContext)
	require.Equal(t, []int64{group.ID}, got.GroupIDs)
	require.Equal(t, 8.0, got.Usage5h)
}
