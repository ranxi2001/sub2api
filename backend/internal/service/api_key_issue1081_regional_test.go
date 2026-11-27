//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/regional"
	"github.com/stretchr/testify/require"
)

type issue1081RegionalSource struct {
	regionalAuthSourceStub
	issue1081Policy
}

func TestIssue1081RegionalSignedPolicyRoundTripRevocationAndExpiry(t *testing.T) {
	ctx := context.Background()
	lookup := bytes.Repeat([]byte{1}, 32)
	codec, err := regional.NewCodec(map[string][]byte{"test": bytes.Repeat([]byte{2}, 32)})
	require.NoError(t, err)
	replica, err := regional.OpenReplica(filepath.Join(t.TempDir(), "auth"), regional.Config{RegionID: "us", MaxSnapshotAgeSeconds: 60}, regional.SnapshotAuth, codec, RegionalAuthValidator([]int64{2}), true)
	require.NoError(t, err)
	reader, err := NewRegionalAuthReader(replica, lookup)
	require.NoError(t, err)
	record := authTestRecord()
	record.APIKey.GroupIDs = []int64{*record.APIKey.GroupID, 99}
	source := &issue1081RegionalSource{regionalAuthSourceStub: regionalAuthSourceStub{records: []RegionalAuthRecord{record}}}
	publish := func(revision uint64, lifetime time.Duration) []byte {
		payload, err := ExportRegionalAuth(ctx, source, []int64{2}, lookup)
		require.NoError(t, err)
		now := time.Now()
		require.NoError(t, replica.Publish(ctx, "test", revision, now, now.Add(lifetime), payload))
		return payload
	}
	for i, enabled := range []bool{true, false, true, false} {
		source.enabled = enabled
		payload := publish(uint64(i+1), time.Minute)
		require.NotContains(t, string(payload), record.APIKey.Key)
		key, err := reader.GetByKey(ctx, record.APIKey.Key)
		require.NoError(t, err)
		expected := []int64{*record.APIKey.GroupID}
		if enabled {
			expected = record.APIKey.GroupIDs
		}
		require.Equal(t, expected, key.ConfiguredGroupIDs())
		require.Equal(t, []int64{*record.APIKey.GroupID, 99}, source.records[0].APIKey.GroupIDs)
	}
	// A legacy document without the new policy keeps multi-group enabled.
	payload, err := ExportRegionalAuth(ctx, source.regionalAuthSourceStub, []int64{2}, lookup)
	require.NoError(t, err)
	var legacy map[string]any
	require.NoError(t, json.Unmarshal(payload, &legacy))
	require.NotContains(t, legacy, "api_key_multi_group_enabled")
	now := time.Now()
	require.NoError(t, replica.Publish(ctx, "test", 5, now, now.Add(time.Minute), payload))
	key, err := reader.GetByKey(ctx, record.APIKey.Key)
	require.NoError(t, err)
	require.Len(t, key.ConfiguredGroupIDs(), 2)
	source.enabled = false
	record.APIKey.User.AllowedGroups = nil
	source.records = []RegionalAuthRecord{record}
	publish(6, time.Minute)
	_, err = reader.GetByKey(ctx, record.APIKey.Key)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	source.records = nil
	publish(7, time.Minute)
	_, err = reader.GetByKey(ctx, record.APIKey.Key)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	source.issue1081Policy.err = ErrAPIKeyGroupPolicyUnavailable
	_, err = ExportRegionalAuth(ctx, source, []int64{2}, lookup)
	require.Error(t, err)
	source.issue1081Policy.err = nil
	source.records = []RegionalAuthRecord{authTestRecord()}
	publish(8, 100*time.Millisecond)
	require.Eventually(t, func() bool { _, err := reader.GetByKey(ctx, source.records[0].APIKey.Key); return err != nil }, time.Second, 10*time.Millisecond)
}
