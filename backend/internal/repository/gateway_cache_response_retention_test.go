package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIssue758RedisResponseOwnerRetentionSurvivesQuotaWait(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	ctx := context.Background()
	writer := service.NewOpenAIWSStateStore(cache)
	retention, ok := writer.(service.OpenAIWSResponseAccountRetentionStore)
	require.True(t, ok)
	const responseID = "resp_fixture_758"
	require.NoError(t, writer.BindResponseAccount(ctx, 7, responseID, 101, time.Minute))
	keys := mr.Keys()
	require.Len(t, keys, 1)
	require.NotContains(t, keys[0], responseID)
	refreshed, err := retention.RefreshResponseAccount(ctx, 7, responseID, 101, 6*time.Hour)
	require.NoError(t, err)
	require.True(t, refreshed)
	require.Equal(t, 6*time.Hour, mr.TTL(keys[0]))

	mr.FastForward(5 * time.Hour)
	reader := service.NewOpenAIWSStateStore(cache)
	owner, err := reader.GetResponseAccount(ctx, 7, responseID)
	require.NoError(t, err)
	require.EqualValues(t, 101, owner, "a fresh application node retains the response owner beyond the original TTL")
	refreshed, err = retention.RefreshResponseAccount(ctx, 7, responseID, 101, time.Minute)
	require.NoError(t, err)
	require.True(t, refreshed)
	require.Equal(t, time.Hour, mr.TTL(keys[0]), "a shorter refresh cannot reduce the remaining recovery window")

	for _, groupID := range []int64{7, 8} {
		refreshed, err = retention.RefreshResponseAccount(ctx, groupID, responseID, 102, 8*time.Hour)
		require.NoError(t, err)
		require.False(t, refreshed)
	}
	require.Equal(t, time.Hour, mr.TTL(keys[0]))
	require.Len(t, mr.Keys(), 1, "a foreign group cannot gain a response anchor")
	mr.FastForward(2 * time.Hour)
	refreshed, err = retention.RefreshResponseAccount(ctx, 7, responseID, 101, 8*time.Hour)
	require.NoError(t, err)
	require.False(t, refreshed, "an expired shared record cannot be revived by the writer's local hot cache")
	require.Empty(t, mr.Keys())
	owner, err = writer.GetResponseAccount(ctx, 7, responseID)
	require.NoError(t, err)
	require.Zero(t, owner)
}

func TestIssue758RedisResponseOwnerRetentionPreservesNewerAndPersistentBindings(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	ctx := context.Background()
	const sessionHash = "openai:response:fixture-digest"
	key := buildSessionKey(7, sessionHash)
	const accountID = int64(9007199254740993)
	require.NoError(t, cache.SetSessionAccountID(ctx, 7, sessionHash, accountID, time.Hour))
	refreshed, err := cache.RefreshSessionAccountID(ctx, 7, sessionHash, accountID-1, 8*time.Hour)
	require.NoError(t, err)
	require.False(t, refreshed, "int64 identities must not be rounded by Lua number comparisons")
	require.Equal(t, time.Hour, mr.TTL(key))
	refreshed, err = cache.RefreshSessionAccountID(ctx, 7, sessionHash, accountID, 8*time.Hour)
	require.NoError(t, err)
	require.True(t, refreshed)
	require.Equal(t, 8*time.Hour, mr.TTL(key))

	require.NoError(t, cache.SetSessionAccountID(ctx, 7, sessionHash, accountID+1, time.Hour))
	refreshed, err = cache.RefreshSessionAccountID(ctx, 7, sessionHash, accountID, 8*time.Hour)
	require.NoError(t, err)
	require.False(t, refreshed, "a stale recovery cannot change a newer response owner")
	owner, err := cache.GetSessionAccountID(ctx, 7, sessionHash)
	require.NoError(t, err)
	require.Equal(t, accountID+1, owner)

	require.NoError(t, cache.SetSessionAccountID(ctx, 7, sessionHash, accountID, 0))
	refreshed, err = cache.RefreshSessionAccountID(ctx, 7, sessionHash, accountID, time.Hour)
	require.NoError(t, err)
	require.True(t, refreshed)
	require.Zero(t, mr.TTL(key), "persistent records must remain persistent")
	mr.Close()
	refreshed, err = cache.RefreshSessionAccountID(ctx, 7, sessionHash, accountID, time.Hour)
	require.Error(t, err)
	require.False(t, refreshed, "store failures cannot become successful retention")
}

func TestIssue758RedisResponseOwnerRetentionRejectsInvalidInput(t *testing.T) {
	cache, mr := newHTTPResponseOwnerTestCache(t)
	for _, test := range []struct {
		sessionHash string
		accountID   int64
		ttl         time.Duration
	}{
		{" ", 101, time.Hour},
		{strings.Repeat("a", 64), 0, time.Hour},
		{strings.Repeat("a", 64), 101, 0},
	} {
		refreshed, err := cache.RefreshSessionAccountID(context.Background(), 7, test.sessionHash, test.accountID, test.ttl)
		require.Error(t, err)
		require.False(t, refreshed)
	}
	require.Empty(t, mr.Keys())
}
