//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type issue1081Policy struct {
	enabled bool
	err     error
}

func (p *issue1081Policy) APIKeyMultiGroupEnabled(context.Context) (bool, error) {
	return p.enabled, p.err
}

func TestIssue1081WarmAuthCacheRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, l1 := range []bool{false, true} {
		t.Run(map[bool]string{true: "L1", false: "L2"}[l1], func(t *testing.T) {
			id := int64(1)
			key := &APIKey{ID: 11, UserID: 7, GroupID: &id, GroupIDs: []int64{1, 2}, Group: &Group{ID: 1, Status: StatusActive}, User: &User{ID: 7, Status: StatusActive}, Status: StatusActive}
			cache := &authCacheStub{}
			cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60}}
			if l1 {
				cfg.APIKeyAuth.L1Size = 1000
				cfg.APIKeyAuth.L1TTLSeconds = 60
			}
			calls := 0
			svc := NewAPIKeyService(nil, nil, nil, nil, nil, cache, cfg)
			t.Cleanup(func() {
				if svc.authCacheL1 != nil {
					svc.authCacheL1.Close()
				}
			})
			entry := &APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(ctx, key)}
			cache.getAuthCache = func(context.Context, string) (*APIKeyAuthCacheEntry, error) { calls++; return entry, nil }
			policy := &issue1081Policy{enabled: true}
			svc.SetGroupPolicy(policy)
			for _, enabled := range []bool{true, false, true, false} {
				policy.enabled = enabled
				got, err := svc.GetByKey(ctx, "fixture-key")
				require.NoError(t, err)
				want := []int64{1}
				if enabled {
					want = []int64{1, 2}
				}
				require.Equal(t, want, got.ConfiguredGroupIDs())
				require.Equal(t, []int64{1, 2}, entry.Snapshot.GroupIDs)
				if svc.authCacheL1 != nil {
					svc.authCacheL1.Wait()
				}
			}
			require.Positive(t, calls)
			if l1 {
				require.Equal(t, 1, calls, "later reads must actually use warm L1")
			}
			policy.err = ErrAPIKeyGroupPolicyUnavailable
			_, err := svc.GetByKey(ctx, "fixture-key")
			require.ErrorIs(t, err, ErrAPIKeyGroupPolicyUnavailable)
		})
	}
}

func TestIssue1081PrimaryOnlyNoProbeAndPinRevocation(t *testing.T) {
	id := int64(1)
	groups := map[int64]*Group{1: {ID: 1, Status: StatusActive, RateMultiplier: 1}, 2: {ID: 2, Status: StatusActive, RateMultiplier: 0.01}}
	key := &APIKey{ID: 11, UserID: 7, GroupID: &id, GroupIDs: []int64{1, 2}, Group: groups[1], User: &User{ID: 7}}
	policy := &issue1081Policy{enabled: false}
	svc := &APIKeyService{groupPolicy: policy, groupRepo: multiGroupRepoStub{groups: groups}}
	// A nil router would reject multi-group and cannot probe or queue an extra.
	for _, kind := range []string{"token", "image", "video"} {
		got, err := svc.SelectRequestGroup(context.Background(), key, nil, kind, nil, nil, true)
		require.NoError(t, err)
		require.Equal(t, int64(1), *got.GroupID)
		require.Equal(t, []int64{1}, got.ConfiguredGroupIDs())
	}
	_, err := svc.PinAPIKeyGroup(context.Background(), key, 2)
	require.Equal(t, "API_KEY_GROUP_CHANGED", infraerrors.Reason(err))
	got, err := svc.PinAPIKeyGroup(context.Background(), key, 1)
	require.NoError(t, err)
	require.Equal(t, id, *got.GroupID)
	groups[1].IsExclusive = true
	_, err = svc.PinAPIKeyGroup(context.Background(), key, 1)
	require.ErrorIs(t, err, ErrGroupNotAllowed)
	groups[1].IsExclusive = false
	key.GroupID = nil
	_, err = svc.applyGroupPolicy(context.Background(), key)
	require.ErrorIs(t, err, ErrAPIKeyPrimaryGroupRequired)
	key.GroupID = &id
	key.Group = nil
	_, err = svc.applyGroupPolicy(context.Background(), key)
	require.ErrorIs(t, err, ErrAPIKeyPrimaryGroupRequired)
	require.Equal(t, []int64{1, 2}, key.GroupIDs)
}

type issue1081UserRepo struct {
	UserRepository
	user *User
}

func (r issue1081UserRepo) GetByID(context.Context, int64) (*User, error) { return r.user, nil }

type issue1081GroupRepo struct{ multiGroupRepoStub }

func (r issue1081GroupRepo) GetByID(ctx context.Context, id int64) (*Group, error) {
	return r.GetByIDLite(ctx, id)
}

func TestIssue1081WritesRejectExplicitMultiAndKeepOmittedBindings(t *testing.T) {
	id := int64(1)
	other := int64(3)
	name := "renamed"
	key := &APIKey{ID: 11, UserID: 7, Key: "fixture-key", GroupID: &id, GroupIDs: []int64{1, 2}, Status: StatusActive, QuotaUsed: 12, Usage5h: 4}
	svc, repo := newUpdateFieldsAPIKeyService(key)
	svc.SetGroupPolicy(&issue1081Policy{enabled: false})
	svc.userRepo = issue1081UserRepo{user: &User{ID: 7}}
	svc.groupRepo = issue1081GroupRepo{multiGroupRepoStub{groups: map[int64]*Group{1: {ID: 1}, 2: {ID: 2}, 3: {ID: 3}, 4: {ID: 4, IsExclusive: true}}}}
	for _, ids := range [][]int64{{1, 2}, {2}, {1, 1}} {
		_, err := svc.Create(context.Background(), 7, CreateAPIKeyRequest{GroupID: &id, GroupIDs: ids})
		require.Equal(t, "API_KEY_MULTI_GROUP_DISABLED", infraerrors.Reason(err))
		require.Equal(t, 400, infraerrors.Code(err))
		_, err = svc.Update(context.Background(), 11, 7, UpdateAPIKeyRequest{GroupID: &id, GroupIDs: &ids})
		require.Equal(t, "API_KEY_MULTI_GROUP_DISABLED", infraerrors.Reason(err))
	}
	for _, req := range []UpdateAPIKeyRequest{{Name: &name}, {Name: &name, GroupID: &id}, {GroupID: &other}} {
		got, err := svc.Update(context.Background(), 11, 7, req)
		require.NoError(t, err)
		want := []int64{1, 2}
		if req.GroupID == &other {
			want = []int64{3, 2}
		}
		require.Equal(t, want, got.GroupIDs)
		require.Equal(t, float64(12), got.QuotaUsed)
		require.Equal(t, float64(4), got.Usage5h)
	}
	require.False(t, repo.updateFields[0].GroupID)
	_, err := svc.Update(context.Background(), 11, 8, UpdateAPIKeyRequest{Name: &name})
	require.ErrorIs(t, err, ErrInsufficientPerms)
	denied := int64(4)
	_, err = svc.Update(context.Background(), 11, 7, UpdateAPIKeyRequest{GroupID: &denied})
	require.ErrorIs(t, err, ErrGroupNotAllowed)
}

func TestIssue1081LegacyCreateDefaultAndExplicitSingle(t *testing.T) {
	ctx := context.Background()
	id := int64(1)
	for _, enabled := range []bool{true, false} {
		repo, cache := newCreateLimitStubs()
		svc := newCreateLimitService(repo, cache, 0, 0)
		svc.SetGroupPolicy(&issue1081Policy{enabled: enabled})
		svc.groupRepo = issue1081GroupRepo{multiGroupRepoStub{groups: map[int64]*Group{1: {ID: 1, Status: StatusActive}, 2: {ID: 2, Status: StatusActive}}}}
		for _, ids := range [][]int64{nil, {1}} {
			got, err := svc.Create(ctx, 7, CreateAPIKeyRequest{Name: "single", GroupID: &id, GroupIDs: ids})
			require.NoError(t, err)
			require.Equal(t, []int64{1}, got.GroupIDs)
		}
		if enabled {
			got, err := svc.Create(ctx, 7, CreateAPIKeyRequest{Name: "multi", GroupID: &id, GroupIDs: []int64{1, 2}})
			require.NoError(t, err)
			require.Equal(t, []int64{1, 2}, got.GroupIDs)
		}
	}
}

func TestIssue1081UncachedAuthAndOldDataFailClosed(t *testing.T) {
	ctx := context.Background()
	id := int64(1)
	original := &APIKey{ID: 11, UserID: 7, GroupID: &id, GroupIDs: []int64{1, 2}, Group: &Group{ID: 1, Status: StatusActive}, User: &User{ID: 7, Status: StatusActive}, Status: StatusActive}
	repo := &authRepoStub{getByKeyForAuth: func(context.Context, string) (*APIKey, error) { copy := *original; return &copy, nil }}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, nil, &config.Config{})
	policy := &issue1081Policy{}
	svc.SetGroupPolicy(policy)
	got, err := svc.GetByKeyUncached(ctx, "fixture-key")
	require.NoError(t, err)
	require.Equal(t, []int64{1}, got.ConfiguredGroupIDs())
	original.GroupIDs = nil
	got, err = svc.GetByKeyUncached(ctx, "fixture-key")
	require.NoError(t, err)
	require.Equal(t, []int64{1}, got.ConfiguredGroupIDs())
	original.GroupID = nil
	original.Group = nil
	original.GroupIDs = []int64{2}
	_, err = svc.GetByKeyUncached(ctx, "fixture-key")
	require.ErrorIs(t, err, ErrAPIKeyPrimaryGroupRequired)
}

type issue1081ChangingPolicy struct{ reads int }

func (p *issue1081ChangingPolicy) APIKeyMultiGroupEnabled(context.Context) (bool, error) {
	p.reads++
	return p.reads == 1, nil
}
func TestIssue1081PinHasOnePolicySnapshotAndPreservesPrimaryIdentity(t *testing.T) {
	id := int64(1)
	fallback := int64(2)
	groups := map[int64]*Group{1: {ID: 1, Status: StatusActive, FallbackGroupID: &fallback, FallbackGroupIDOnInvalidRequest: &fallback}, 2: {ID: 2, Status: StatusActive}}
	key := &APIKey{GroupID: &id, GroupIDs: []int64{1, 2}, Group: groups[1], User: &User{ID: 7}}
	policy := &issue1081ChangingPolicy{}
	svc := &APIKeyService{groupRepo: multiGroupRepoStub{groups: groups}, groupPolicy: policy}
	_, err := svc.PinAPIKeyGroup(context.Background(), key, 2)
	require.NoError(t, err)
	require.Equal(t, 1, policy.reads, "one atomic policy view before selecting a group")
	_, err = svc.PinAPIKeyGroup(context.Background(), key, 2)
	require.Error(t, err, "next pin sees disabled and refuses extra")
	got, err := svc.PinAPIKeyGroup(context.Background(), key, 1)
	require.NoError(t, err)
	require.Nil(t, got.Group.FallbackGroupID)
	require.Nil(t, got.Group.FallbackGroupIDOnInvalidRequest)
	require.NotNil(t, key.Group.FallbackGroupID, "persistent/cache group is untouched")
}
