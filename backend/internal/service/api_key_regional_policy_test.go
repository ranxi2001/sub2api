//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// This checks the real regional export/validation schema without relying on
// directory fsync, which the regional persistence layer requires on its hosts.
func TestAPIKeyRegionalModelPolicyExportRoundtrip(t *testing.T) {
	configured := authTestRecord()
	configured.APIKey.ModelMapping = map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}
	configured.APIKey.ReasoningEffort = "high"
	configured.APIKey.AllowLongContext = false
	plain := authTestRecord()
	plain.APIKey.ID = 11
	plain.APIKey.Key = "sk-regional-plain-fixture"
	plain.APIKey.AllowLongContext = true
	lookup := bytes.Repeat([]byte{1}, 32)
	payload, err := ExportRegionalAuth(context.Background(), regionalAuthSourceStub{
		records: []RegionalAuthRecord{configured, plain},
	}, []int64{2}, lookup)
	require.NoError(t, err)
	require.NotContains(t, string(payload), configured.APIKey.Key)
	require.NotContains(t, string(payload), plain.APIKey.Key)
	require.NotContains(t, string(payload), "private@example.test")
	require.NoError(t, RegionalAuthValidator([]int64{2})(payload))
	doc, err := decodeRegionalAuth(payload)
	require.NoError(t, err)
	require.Len(t, doc.Entries, 2)
	require.Equal(t, apiKeyAuthSnapshotVersion, doc.Entries[0].Snapshot.Version)
	require.Equal(t, configured.APIKey.ModelMapping, doc.Entries[0].Snapshot.ModelMapping)
	require.Equal(t, "high", doc.Entries[0].Snapshot.ReasoningEffort)
	require.False(t, doc.Entries[0].Snapshot.AllowLongContext)
	require.True(t, doc.Entries[1].Snapshot.AllowLongContext)
	require.Empty(t, doc.Entries[1].Snapshot.ModelMapping)
	require.Empty(t, doc.Entries[1].Snapshot.ReasoningEffort)

	mapper := &APIKeyService{}
	key := mapper.snapshotToAPIKey(configured.APIKey.Key, doc.Entries[0].Snapshot)
	require.Equal(t, configured.APIKey.ModelMapping, key.ModelMapping)
	require.Equal(t, "high", key.ReasoningEffort)
	require.False(t, key.AllowLongContext)
	key.ModelMapping["gpt-6.1-sol"] = "changed"
	require.Equal(t, "gpt-5.6-sol", doc.Entries[0].Snapshot.ModelMapping["gpt-6.1-sol"])
	plainKey := mapper.snapshotToAPIKey(plain.APIKey.Key, doc.Entries[1].Snapshot)
	require.Empty(t, plainKey.ModelMapping)
	require.Empty(t, plainKey.ReasoningEffort)
	require.True(t, plainKey.AllowLongContext)

	doc.Entries[0].Snapshot.Version = 32
	stale, err := json.Marshal(doc)
	require.NoError(t, err)
	require.Error(t, RegionalAuthValidator([]int64{2})(stale), "regional snapshots predating long context permission must be refreshed")
}
