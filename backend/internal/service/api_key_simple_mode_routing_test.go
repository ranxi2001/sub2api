//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

const issue1081RoutingModel = "gpt-5.1"

func issue1081RoutingContext(t *testing.T, enabled bool, bindings []int64) (context.Context, *Group) {
	t.Helper()
	id := int64(1)
	group := &Group{ID: id, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true}
	original := &APIKey{ID: 11, UserID: 7, GroupID: &id, GroupIDs: bindings, Group: group, User: &User{ID: 7, Status: StatusActive}, Status: StatusActive}
	keys := &APIKeyService{groupPolicy: &issue1081Policy{enabled: enabled}}
	projected, err := keys.applyGroupPolicy(context.Background(), original)
	require.NoError(t, err)
	require.Equal(t, bindings, original.GroupIDs, "policy projection must preserve shared auth bindings")
	ctx := context.WithValue(context.Background(), ctxkey.Group, projected.Group)
	return WithAPIKeyGroupConstraint(ctx, projected), projected.Group
}

func issue1081RoutingAccount(id, group int64, priority int) Account {
	return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: priority, Status: StatusActive, Schedulable: true, Concurrency: 5,
		GroupIDs: []int64{group}, AccountGroups: []AccountGroup{{GroupID: group}}, Credentials: map[string]any{"model_mapping": map[string]any{issue1081RoutingModel: issue1081RoutingModel}}}
}

func TestIssue1081SimpleModeSupportedModelRouting(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		bindings []int64
		main     bool
		want     int64
	}{
		{"disabled_extra_only", false, []int64{1, 2}, false, 0},
		{"disabled_main_eligible_extra_better_sticky", false, []int64{1, 2}, true, 10},
		{"enabled_legacy", true, []int64{1, 2}, false, 20},
		{"single_group_legacy", false, []int64{1}, false, 20},
		{"duplicate_primary_is_not_extra", false, []int64{1, 1}, false, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, loadAware := range []bool{false, true} {
				t.Run(map[bool]string{false: "legacy", true: "load"}[loadAware], func(t *testing.T) {
					ctx, group := issue1081RoutingContext(t, tc.enabled, tc.bindings)
					accounts := []Account{issue1081RoutingAccount(20, 2, 1)}
					if tc.main {
						accounts = append(accounts, issue1081RoutingAccount(10, 1, 10))
					}
					repo := newGroupAwareMockRepo(accounts)
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Gateway.Scheduling.LoadBatchEnabled = true
					slots := &mockConcurrencyCache{}
					svc := &GatewayService{accountRepo: repo, groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{1: group}}, cfg: cfg, cache: &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sticky": 20}}, concurrencyService: NewConcurrencyService(slots)}
					var account *Account
					var err error
					if loadAware {
						result, selectErr := svc.SelectAccountWithLoadAwareness(ctx, &group.ID, "sticky", issue1081RoutingModel, nil, "", 0)
						err = selectErr
						if result != nil {
							account = result.Account
							if result.ReleaseFunc != nil {
								result.ReleaseFunc()
							}
						}
					} else {
						account, err = svc.SelectAccountForModelWithExclusions(ctx, &group.ID, "sticky", issue1081RoutingModel, nil)
					}
					if tc.want == 0 {
						require.ErrorIs(t, err, ErrNoAvailableAccounts, "ISSUE1081_PRIMARY_POOL: supported extra B must never be selected")
						require.Nil(t, account)
						require.Zero(t, slots.acquireAccountCalls, "extra B must never acquire or queue a slot")
					} else {
						require.NoError(t, err)
						require.NotNil(t, account)
						require.Equal(t, tc.want, account.ID)
					}
					availability, err := svc.ProbeAPIKeyGroup(ctx, group, issue1081RoutingModel, "token")
					require.NoError(t, err)
					require.Equal(t, tc.want != 0, availability.Available, "extra B must not contribute probe capacity")
					diagnosis := svc.DiagnoseModelAvailabilityForPlatform(ctx, &group.ID, issue1081RoutingModel, PlatformOpenAI)
					require.True(t, diagnosis.Complete)
					require.Equal(t, tc.want != 0, diagnosis.HasAccountsInPool, "extra B must not contribute diagnosis")
					require.Equal(t, tc.want != 0, diagnosis.HasModelSupport)
				})
			}
		})
	}
}

func TestIssue1081SimpleModeConstraintDoesNotMutateSharedCandidates(t *testing.T) {
	ctx, _ := issue1081RoutingContext(t, false, []int64{1, 2})
	shared := []Account{issue1081RoutingAccount(20, 2, 1), issue1081RoutingAccount(10, 1, 10)}
	filtered := filterAPIKeyGroupAccounts(ctx, shared)
	require.Len(t, filtered, 1)
	require.Equal(t, int64(10), filtered[0].ID)
	filtered[0].Priority = 999
	require.Equal(t, int64(20), shared[0].ID)
	require.Equal(t, 10, shared[1].Priority)
	require.Equal(t, []int64{2}, shared[0].GroupIDs)
}
