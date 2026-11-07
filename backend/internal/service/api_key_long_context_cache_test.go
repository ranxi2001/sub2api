package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyLongContextAuthCacheRoundtrip(t *testing.T) {
	svc := &APIKeyService{}
	for _, enabled := range []bool{false, true} {
		key := &APIKey{ID: 10, UserID: 20, User: &User{ID: 20}, AllowLongContext: enabled}
		snapshot := svc.snapshotFromAPIKey(context.Background(), key)
		payload, err := json.Marshal(APIKeyAuthCacheEntry{Snapshot: snapshot})
		require.NoError(t, err)
		require.Contains(t, string(payload), `"allow_long_context":`)
		var cached APIKeyAuthCacheEntry
		require.NoError(t, json.Unmarshal(payload, &cached))
		got, used, err := svc.applyAuthCacheEntry("sk-context-fixture", &cached)
		require.NoError(t, err)
		require.True(t, used)
		require.Equal(t, enabled, got.AllowLongContext)
	}
	_, used, err := svc.applyAuthCacheEntry("sk-context-fixture", &APIKeyAuthCacheEntry{Snapshot: &APIKeyAuthSnapshot{Version: 32}})
	require.NoError(t, err)
	require.False(t, used, "v32 omitted the permission and must be fetched again")
}
