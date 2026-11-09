package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIssue758ResponseOwnerLocalRetention(t *testing.T) {
	store, ok := NewOpenAIWSStateStore(nil).(*defaultOpenAIWSStateStore)
	require.True(t, ok)
	ctx := context.Background()
	const groupID, accountID = int64(7), int64(101)
	const responseID = "resp_fixture_758"
	key := openAIWSResponseAccountMapKey(groupID, responseID)
	require.NoError(t, store.BindResponseAccount(ctx, groupID, responseID, accountID, time.Minute))
	refreshed, err := store.RefreshResponseAccount(ctx, groupID, responseID, accountID, 6*time.Hour)
	require.NoError(t, err)
	require.True(t, refreshed)
	retained := store.responseToAccount[key]
	require.Equal(t, accountID, retained.accountID)
	require.True(t, retained.expiresAt.After(time.Now().Add(5*time.Hour)))

	refreshed, err = store.RefreshResponseAccount(ctx, groupID, responseID, accountID, time.Second)
	require.NoError(t, err)
	require.True(t, refreshed)
	require.Equal(t, retained, store.responseToAccount[key], "retention must not shorten an existing deadline")
	for _, change := range []struct {
		groupID    int64
		responseID string
		accountID  int64
	}{
		{groupID + 1, responseID, accountID},
		{groupID, responseID + "_other", accountID},
		{groupID, responseID, accountID + 1},
	} {
		refreshed, err = store.RefreshResponseAccount(ctx, change.groupID, change.responseID, change.accountID, 8*time.Hour)
		require.NoError(t, err)
		require.False(t, refreshed)
		require.Len(t, store.responseToAccount, 1, "misses cannot create owner records")
		require.Equal(t, retained, store.responseToAccount[key])
	}

	expired := retained
	expired.expiresAt = time.Now().Add(-time.Minute)
	store.responseToAccount[key] = expired
	refreshed, err = store.RefreshResponseAccount(ctx, groupID, responseID, accountID, 8*time.Hour)
	require.NoError(t, err)
	require.False(t, refreshed, "expired evidence cannot be revived")
	require.Equal(t, expired, store.responseToAccount[key])
}

type issue758ResponseRetentionCache struct {
	stubGatewayCache
	refreshed bool
	err       error
	groupID   int64
	key       string
	accountID int64
	ttl       time.Duration
	deadline  bool
}

func (c *issue758ResponseRetentionCache) RefreshSessionAccountID(ctx context.Context, groupID int64, key string, accountID int64, ttl time.Duration) (bool, error) {
	c.groupID, c.key, c.accountID, c.ttl = groupID, key, accountID, ttl
	_, c.deadline = ctx.Deadline()
	return c.refreshed, c.err
}

func TestIssue758ResponseOwnerSharedRetentionFailsClosed(t *testing.T) {
	ctx := context.Background()
	for _, outcome := range []string{"success", "miss", "error", "unsupported"} {
		t.Run(outcome, func(t *testing.T) {
			cache := &issue758ResponseRetentionCache{refreshed: outcome == "success"}
			if outcome == "error" {
				cache.err = errors.New("fixture store unavailable")
			}
			var adapter GatewayCache = cache
			if outcome == "unsupported" {
				adapter = &stubGatewayCache{}
			}
			store, ok := NewOpenAIWSStateStore(adapter).(*defaultOpenAIWSStateStore)
			require.True(t, ok)
			require.NoError(t, store.BindResponseAccount(ctx, 7, "resp_fixture_758", 101, time.Minute))
			key := openAIWSResponseAccountMapKey(7, "resp_fixture_758")
			before := store.responseToAccount[key]
			refreshed, err := store.RefreshResponseAccount(ctx, 7, "resp_fixture_758", 101, 6*time.Hour)
			require.Equal(t, outcome == "success", refreshed)
			if outcome == "error" {
				require.ErrorIs(t, err, cache.err)
			} else {
				require.NoError(t, err)
			}
			if outcome != "unsupported" {
				require.EqualValues(t, 7, cache.groupID)
				require.Equal(t, openAIWSResponseAccountCacheKey("resp_fixture_758"), cache.key)
				require.NotContains(t, cache.key, "resp_fixture_758", "shared keys contain only response digests")
				require.EqualValues(t, 101, cache.accountID)
				require.Equal(t, 6*time.Hour, cache.ttl)
				require.True(t, cache.deadline, "shared retention has an independent bounded deadline")
			}
			switch outcome {
			case "success":
				require.True(t, store.responseToAccount[key].expiresAt.After(time.Now().Add(5*time.Hour)))
			case "miss":
				_, found := store.responseToAccount[key]
				require.False(t, found, "a local hot entry cannot survive an authoritative miss")
			default:
				require.Equal(t, before, store.responseToAccount[key], "an error/unsupported cache cannot pretend retention succeeded")
			}
		})
	}
}

func TestIssue758ResponseOwnerRetentionRejectsCanceledOrInvalidRequests(t *testing.T) {
	store, ok := NewOpenAIWSStateStore(nil).(*defaultOpenAIWSStateStore)
	require.True(t, ok)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	refreshed, err := store.RefreshResponseAccount(ctx, 7, "resp_fixture_758", 101, time.Hour)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, refreshed)
	for _, test := range []struct {
		responseID string
		accountID  int64
		ttl        time.Duration
	}{
		{" ", 101, time.Hour},
		{"resp_fixture_758", 0, time.Hour},
		{"resp_fixture_758", 101, 0},
	} {
		refreshed, err = store.RefreshResponseAccount(context.Background(), 7, test.responseID, test.accountID, test.ttl)
		require.Error(t, err)
		require.False(t, refreshed)
	}
	require.Empty(t, store.responseToAccount)
}
