//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/regional"
	"github.com/stretchr/testify/require"
)

type regionalAuthSourceStub struct {
	records []RegionalAuthRecord
	err     error
}

func (s regionalAuthSourceStub) ListRegionalAuth(context.Context, []int64) ([]RegionalAuthRecord, error) {
	return s.records, s.err
}
func authTestRecord() RegionalAuthRecord {
	groupID := int64(3)
	rpm := 7
	return RegionalAuthRecord{APIKey: &APIKey{ID: 1, UserID: 2, GroupID: &groupID, Key: "sk-regional-test-secret", Status: StatusActive, IPWhitelist: []string{"127.0.0.1"}, User: &User{ID: 2, Status: StatusActive, Role: RoleUser, AllowedGroups: []int64{3}, UserGroupRPMOverride: &rpm, UserGroupDeniedModels: []string{"forbidden-model"}, Email: "private@example.test", PasswordHash: "private-hash"}, Group: &Group{ID: 3, Status: StatusActive, Platform: PlatformOpenAI, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard, ProfitControlEnabled: true, StreamOnly: true}}}
}
func TestRegionalAuthLocalReplacementRevocationAndRestore(t *testing.T) {
	ctx := context.Background()
	lookup := bytes.Repeat([]byte{1}, 32)
	codec, err := regional.NewCodec(map[string][]byte{"test": bytes.Repeat([]byte{2}, 32)})
	require.NoError(t, err)
	policy := regional.Config{RegionID: "us", MaxSnapshotAgeSeconds: 60}
	path := filepath.Join(t.TempDir(), "auth")
	replica, err := regional.OpenReplica(path, policy, regional.SnapshotAuth, codec, RegionalAuthValidator([]int64{2}), true)
	require.NoError(t, err)
	reader, err := NewRegionalAuthReader(replica, lookup)
	require.NoError(t, err)
	// Nil repositories/cache prove both lookups cannot fall back to primary.
	svc := &APIKeyService{localAuth: reader}
	_, err = svc.GetByKey(ctx, "sk-regional-test-secret")
	require.ErrorIs(t, err, ErrRegionalAuthUnavailable)
	record := authTestRecord()
	record.APIKey.ModelMapping = map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}
	record.APIKey.ReasoningEffort = "high"
	payload, err := ExportRegionalAuth(ctx, regionalAuthSourceStub{records: []RegionalAuthRecord{record}}, []int64{2}, lookup)
	require.NoError(t, err)
	require.NotContains(t, string(payload), record.APIKey.Key)
	require.NotContains(t, string(payload), "private-hash")
	require.NotContains(t, string(payload), "private@example.test")
	now := time.Now()
	require.NoError(t, replica.Publish(ctx, "test", 1, now, now.Add(time.Minute), payload))
	key, err := svc.GetByKey(ctx, record.APIKey.Key)
	require.NoError(t, err)
	require.Equal(t, []string{"forbidden-model"}, key.User.UserGroupDeniedModels)
	require.Equal(t, 7, *key.User.UserGroupRPMOverride)
	require.True(t, key.Group.ProfitControlEnabled)
	require.True(t, key.Group.StreamOnly)
	require.Equal(t, record.APIKey.ModelMapping, key.ModelMapping)
	require.Equal(t, "high", key.ReasoningEffort)
	key.ModelMapping["gpt-6.1-sol"] = "changed"
	key.User.AllowedGroups = nil
	key.Group.Status = StatusDisabled
	key, err = svc.GetByKeyUncached(ctx, record.APIKey.Key)
	require.NoError(t, err)
	require.Equal(t, []int64{3}, key.User.AllowedGroups)
	require.Equal(t, "gpt-5.6-sol", key.ModelMapping["gpt-6.1-sol"])
	persisted, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(persisted), "forbidden-model")
	empty, err := ExportRegionalAuth(ctx, regionalAuthSourceStub{}, []int64{2}, lookup)
	require.NoError(t, err)
	require.NoError(t, replica.Publish(ctx, "test", 2, now, now.Add(time.Minute), empty))
	_, err = svc.GetByKey(ctx, record.APIKey.Key)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	_, err = svc.GetByKeyUncached(ctx, record.APIKey.Key)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	require.Error(t, replica.Accept(ctx, persisted), "rollback must not resurrect revoked key")
	restored, err := regional.OpenReplica(path, policy, regional.SnapshotAuth, codec, RegionalAuthValidator([]int64{2}), false)
	require.NoError(t, err)
	reader, err = NewRegionalAuthReader(restored, lookup)
	require.NoError(t, err)
	_, err = reader.GetByKey(ctx, record.APIKey.Key)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
}
func TestRegionalAuthDeniesStateAndPermissionChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*RegionalAuthRecord)
	}{
		{"key-disabled", func(r *RegionalAuthRecord) { r.APIKey.Status = StatusDisabled }},
		{"key-expired", func(r *RegionalAuthRecord) { v := time.Now().Add(-time.Second); r.APIKey.ExpiresAt = &v }},
		{"user-disabled", func(r *RegionalAuthRecord) { r.APIKey.User.Status = StatusDisabled }},
		{"group-disabled", func(r *RegionalAuthRecord) { r.APIKey.Group.Status = StatusDisabled }},
		{"group-deleted", func(r *RegionalAuthRecord) { r.APIKey.Group = nil }},
		{"exclusive-revoked", func(r *RegionalAuthRecord) { r.APIKey.User.AllowedGroups = nil }},
		{"public-restricted", func(r *RegionalAuthRecord) {
			r.APIKey.Group.IsExclusive = false
			r.APIKey.User.RestrictPublicGroups = true
			r.APIKey.User.AllowedGroups = nil
		}},
		{"subscription-missing", func(r *RegionalAuthRecord) { r.APIKey.Group.SubscriptionType = SubscriptionTypeSubscription }},
		{"subscription-expired", func(r *RegionalAuthRecord) {
			r.APIKey.Group.SubscriptionType = SubscriptionTypeSubscription
			r.Subscription = &RegionalAuthSubscription{StartsAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-time.Second)}
		}},
		{"subscription-future", func(r *RegionalAuthRecord) {
			r.APIKey.Group.SubscriptionType = SubscriptionTypeSubscription
			r.Subscription = &RegionalAuthSubscription{StartsAt: time.Now().Add(time.Hour), ExpiresAt: time.Now().Add(2 * time.Hour)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codec, err := regional.NewCodec(map[string][]byte{"test": bytes.Repeat([]byte{2}, 32)})
			require.NoError(t, err)
			replica, err := regional.OpenReplica(filepath.Join(t.TempDir(), "auth"), regional.Config{RegionID: "us", MaxSnapshotAgeSeconds: 60}, regional.SnapshotAuth, codec, RegionalAuthValidator([]int64{2}), true)
			require.NoError(t, err)
			lookup := bytes.Repeat([]byte{1}, 32)
			record := authTestRecord()
			tc.change(&record)
			payload, err := ExportRegionalAuth(context.Background(), regionalAuthSourceStub{records: []RegionalAuthRecord{record}}, []int64{2}, lookup)
			require.NoError(t, err)
			now := time.Now()
			require.NoError(t, replica.Publish(context.Background(), "test", 1, now, now.Add(time.Minute), payload))
			reader, err := NewRegionalAuthReader(replica, lookup)
			require.NoError(t, err)
			_, err = reader.GetByKey(context.Background(), record.APIKey.Key)
			require.ErrorIs(t, err, ErrAPIKeyNotFound)
		})
	}
}
func TestRegionalAuthExpiryAndPersistenceFailure(t *testing.T) {
	ctx := context.Background()
	lookup := bytes.Repeat([]byte{1}, 32)
	codec, err := regional.NewCodec(map[string][]byte{"test": bytes.Repeat([]byte{2}, 32)})
	require.NoError(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, "auth")
	replica, err := regional.OpenReplica(path, regional.Config{RegionID: "us", MaxSnapshotAgeSeconds: 60}, regional.SnapshotAuth, codec, RegionalAuthValidator([]int64{2}), true)
	require.NoError(t, err)
	reader, err := NewRegionalAuthReader(replica, lookup)
	require.NoError(t, err)
	record := authTestRecord()
	payload, err := ExportRegionalAuth(ctx, regionalAuthSourceStub{records: []RegionalAuthRecord{record}}, []int64{2}, lookup)
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, replica.Publish(ctx, "test", 1, now, now.Add(150*time.Millisecond), payload))
	require.Eventually(t, func() bool {
		_, err := reader.GetByKey(ctx, record.APIKey.Key)
		return errors.Is(err, ErrRegionalAuthUnavailable)
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, replica.Publish(ctx, "test", 2, time.Now(), time.Now().Add(30*time.Second), payload))
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0700))
	require.Error(t, replica.Publish(ctx, "test", 3, time.Now(), time.Now().Add(30*time.Second), payload))
	_, err = reader.GetByKey(ctx, record.APIKey.Key)
	require.ErrorIs(t, err, ErrRegionalAuthUnavailable)
}
func TestRegionalAuthValidatorAndFailClosedWiring(t *testing.T) {
	lookup := bytes.Repeat([]byte{1}, 32)
	ctx := context.Background()
	_, err := ExportRegionalAuth(ctx, regionalAuthSourceStub{err: errors.New("permission query failed")}, []int64{2}, lookup)
	require.Error(t, err)
	payload, err := ExportRegionalAuth(ctx, regionalAuthSourceStub{records: []RegionalAuthRecord{authTestRecord()}}, []int64{2}, lookup)
	require.NoError(t, err)
	require.Error(t, RegionalAuthValidator(nil)(payload))
	var doc RegionalAuthDocument
	require.NoError(t, json.Unmarshal(payload, &doc))
	doc.Entries = append(doc.Entries, doc.Entries[0])
	duplicate, _ := json.Marshal(doc)
	require.Error(t, RegionalAuthValidator([]int64{2})(duplicate))
	doc.Entries = doc.Entries[:1]
	doc.Entries[0].Snapshot.IPBlacklist = []string{"invalid-ip"}
	invalid, _ := json.Marshal(doc)
	require.Error(t, RegionalAuthValidator([]int64{2})(invalid))
	svc := &APIKeyService{cfg: &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway, RegionalAuthFile: "configured"}}}
	_, err = svc.GetByKey(ctx, "key")
	require.ErrorIs(t, err, ErrRegionalAuthUnavailable)
	_, err = svc.GetByKeyUncached(ctx, "key")
	require.ErrorIs(t, err, ErrRegionalAuthUnavailable)
	_, err = svc.GetRegionalByKey(ctx, "key")
	require.ErrorIs(t, err, ErrRegionalAuthUnavailable)
}

func TestRegionalAuthCredentialExpiryWhileSnapshotFresh(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		t.Run(fmt.Sprint(subscription), func(t *testing.T) {
			ctx := context.Background()
			lookup := bytes.Repeat([]byte{1}, 32)
			codec, err := regional.NewCodec(map[string][]byte{"test": bytes.Repeat([]byte{2}, 32)})
			require.NoError(t, err)
			replica, err := regional.OpenReplica(filepath.Join(t.TempDir(), "auth"), regional.Config{RegionID: "us", MaxSnapshotAgeSeconds: 60}, regional.SnapshotAuth, codec, RegionalAuthValidator([]int64{2}), true)
			require.NoError(t, err)
			record := authTestRecord()
			expires := time.Now().Add(500 * time.Millisecond)
			if subscription {
				record.APIKey.Group.SubscriptionType = SubscriptionTypeSubscription
				record.Subscription = &RegionalAuthSubscription{StartsAt: time.Now().Add(-time.Hour), ExpiresAt: expires}
			} else {
				record.APIKey.ExpiresAt = &expires
			}
			payload, err := ExportRegionalAuth(ctx, regionalAuthSourceStub{records: []RegionalAuthRecord{record}}, []int64{2}, lookup)
			require.NoError(t, err)
			now := time.Now()
			require.NoError(t, replica.Publish(ctx, "test", 1, now, now.Add(time.Minute), payload))
			reader, err := NewRegionalAuthReader(replica, lookup)
			require.NoError(t, err)
			_, err = reader.GetByKey(ctx, record.APIKey.Key)
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				_, err := reader.GetByKey(ctx, record.APIKey.Key)
				return errors.Is(err, ErrAPIKeyNotFound)
			}, 2*time.Second, 10*time.Millisecond)
			_, err = replica.Current()
			require.NoError(t, err, "snapshot remains fresh after credential expiry")
		})
	}
}
