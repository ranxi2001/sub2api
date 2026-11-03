package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthModelPolicyRoundtripAndIsolation(t *testing.T) {
	svc := &APIKeyService{}
	key := &APIKey{ID: 10, UserID: 20, User: &User{ID: 20}, ModelMapping: map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, ReasoningEffort: "high"}
	snapshot := svc.snapshotFromAPIKey(context.Background(), key)
	key.ModelMapping["gpt-6.1-sol"] = "changed"
	require.Equal(t, "gpt-5.6-sol", snapshot.ModelMapping["gpt-6.1-sol"])
	payload, err := json.Marshal(APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))
	first, used, err := svc.applyAuthCacheEntry("sk-fixture", &cached)
	require.NoError(t, err)
	require.True(t, used)
	second, used, err := svc.applyAuthCacheEntry("sk-fixture", &cached)
	require.NoError(t, err)
	require.True(t, used)
	first.ModelMapping["gpt-6.1-sol"] = "changed"
	require.Equal(t, "gpt-5.6-sol", second.ModelMapping["gpt-6.1-sol"])
	require.Equal(t, "high", second.ReasoningEffort)
	require.Equal(t, "gpt-5.6-sol", cached.Snapshot.ModelMapping["gpt-6.1-sol"])

	_, used, err = svc.applyAuthCacheEntry("sk-fixture", &APIKeyAuthCacheEntry{Snapshot: &APIKeyAuthSnapshot{Version: 31}})
	require.NoError(t, err)
	require.False(t, used, "snapshots predating key policy must be fetched again")
}
