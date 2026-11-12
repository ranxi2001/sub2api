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
		key := &APIKey{ID: 10, UserID: 20, User: &User{ID: 20}, AllowLongContext: enabled, ContextTokenLimit: 1234}
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
		require.Equal(t, 1234, got.ContextTokenLimit)
	}
	_, used, err := svc.applyAuthCacheEntry("sk-context-fixture", &APIKeyAuthCacheEntry{Snapshot: &APIKeyAuthSnapshot{Version: 33}})
	require.NoError(t, err)
	require.False(t, used, "v33 omitted the context ceiling and must be fetched again")
}
