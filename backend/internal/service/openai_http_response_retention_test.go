package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIssue758RecoveryUserRequiresAuthenticatedHTTPMarker(t *testing.T) {
	c, _ := newTurnStateTestContext(t, 758, "fixture-session")
	c.Set("api_key", &APIKey{ID: 758, GroupID: int64Ptr(758)})
	svc := &OpenAIGatewayService{}
	recoveryUser := func() int64 {
		ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(`{"input":[]}`), "fixture-session", "gpt-6-astra")
		return openAIAgentRecoveryFromContext(ctx).userID
	}
	require.Zero(t, openAIAgentRecoveryHTTPUser(nil, 758))
	require.Zero(t, recoveryUser(), "WS and other ingress without the HTTP authorization marker must not extend tenant anchors")
	c.Set(openAIHTTPResponseOwnerContextKey, map[string]int64{"userID": 101, "apiKeyID": 758})
	require.Zero(t, recoveryUser(), "untyped request data cannot act as authenticated ownership")
	SetOpenAIHTTPResponseOwner(c, 101, 759)
	require.Zero(t, recoveryUser(), "a marker for another key must not authorize retention")
	SetOpenAIHTTPResponseOwner(c, 101, 758)
	require.EqualValues(t, 101, recoveryUser())
}

func TestIssue758HTTPOwnerLocalRetentionPreservesTenantAndOriginalKey(t *testing.T) {
	store, ok := NewOpenAIWSStateStore(nil).(*defaultOpenAIWSStateStore)
	require.True(t, ok)
	ctx := context.Background()
	const responseID = "resp_fixture_http_758"
	key := openAIWSResponseAccountMapKey(7, responseID)
	require.NoError(t, store.BindHTTPResponseOwner(ctx, 7, responseID, 101, 201, time.Minute))
	refreshed, err := store.RefreshHTTPResponseOwner(ctx, 7, responseID, 101, 301, 6*time.Hour)
	require.NoError(t, err)
	require.True(t, refreshed, "normal continuation permits a different key belonging to the same user")
	retained := store.responseOwners[key]
	require.EqualValues(t, 101, retained.userID)
	require.EqualValues(t, 201, retained.apiKeyID, "retention cannot replace the producer's original key")
	require.True(t, retained.expiresAt.After(time.Now().Add(5*time.Hour)))
	refreshed, err = store.RefreshHTTPResponseOwner(ctx, 7, responseID, 101, 301, time.Minute)
	require.NoError(t, err)
	require.True(t, refreshed)
	require.Equal(t, retained, store.responseOwners[key])

	for _, test := range []struct {
		groupID    int64
		responseID string
		userID     int64
	}{
		{7, responseID, 102},
		{8, responseID, 101},
		{7, responseID + "_other", 101},
	} {
		refreshed, err = store.RefreshHTTPResponseOwner(ctx, test.groupID, test.responseID, test.userID, 201, 8*time.Hour)
		require.NoError(t, err)
		require.False(t, refreshed)
		require.Len(t, store.responseOwners, 1)
		require.Equal(t, retained, store.responseOwners[key])
	}
	expired := retained
	expired.expiresAt = time.Now().Add(-time.Minute)
	store.responseOwners[key] = expired
	refreshed, err = store.RefreshHTTPResponseOwner(ctx, 7, responseID, 101, 201, 8*time.Hour)
	require.NoError(t, err)
	require.False(t, refreshed)
	require.Equal(t, expired, store.responseOwners[key], "an expired authorization cannot be recreated")
}

func TestIssue758HTTPOwnerRetentionRejectsLegacyAdaptersAndInvalidRequests(t *testing.T) {
	ctx := context.Background()
	store := NewOpenAIWSStateStore(&stubGatewayCache{})
	require.NoError(t, store.BindHTTPResponseOwner(ctx, 7, "resp_fixture_http_758", 101, 201, time.Minute))
	retention, ok := store.(OpenAIHTTPResponseOwnerRetentionStore)
	require.True(t, ok)
	refreshed, err := retention.RefreshHTTPResponseOwner(ctx, 7, "resp_fixture_http_758", 101, 201, 8*time.Hour)
	require.NoError(t, err)
	require.False(t, refreshed, "legacy adapters cannot pretend that shared retention is atomic")

	local, ok := NewOpenAIWSStateStore(nil).(OpenAIHTTPResponseOwnerRetentionStore)
	require.True(t, ok)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	refreshed, err = local.RefreshHTTPResponseOwner(canceled, 7, "resp_fixture_http_758", 101, 201, time.Hour)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, refreshed)
	for _, test := range []struct {
		groupID    int64
		responseID string
		userID     int64
		keyID      int64
		ttl        time.Duration
	}{
		{-1, "resp_fixture_http_758", 101, 201, time.Hour},
		{7, " ", 101, 201, time.Hour},
		{7, "resp_fixture_http_758", 0, 201, time.Hour},
		{7, "resp_fixture_http_758", 101, 0, time.Hour},
		{7, "resp_fixture_http_758", 101, 201, 0},
	} {
		refreshed, err = local.RefreshHTTPResponseOwner(ctx, test.groupID, test.responseID, test.userID, test.keyID, test.ttl)
		require.Error(t, err)
		require.False(t, refreshed)
	}
}
