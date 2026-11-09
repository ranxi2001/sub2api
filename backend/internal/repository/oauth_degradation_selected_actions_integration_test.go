//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func degradationActionFlag(v bool) *bool { return &v }

func TestOAuthDegradationSelectedActionsPostgres(t *testing.T) {
	for mask := 1; mask < 8; mask++ {
		t.Run(fmt.Sprintf("action-mask-%d", mask), func(t *testing.T) {
			ctx := t.Context()
			repo, c := degradationPolicyFixture(t)
			suffix := fmt.Sprint(time.Now().UnixNano())
			selected := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "degradation-selected-" + suffix, Platform: service.PlatformOpenAI})
			retained := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "degradation-retained-" + suffix, Platform: service.PlatformOpenAI})
			absent := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "degradation-absent-" + suffix, Platform: service.PlatformOpenAI})
			t.Cleanup(func() {
				for _, id := range []int64{selected.ID, retained.ID, absent.ID} {
					require.NoError(t, integrationEntClient.Group.DeleteOneID(id).Exec(context.Background()))
				}
			})
			concurrency, models, groups := mask&1 != 0, mask&2 != 0, mask&4 != 0
			c.DegradationAction.LimitConcurrency, c.DegradationAction.LimitModels = degradationActionFlag(concurrency), degradationActionFlag(models)
			c.DegradationAction.LimitGroups, c.DegradationAction.GroupIDs = groups, []int64{selected.ID, absent.ID}
			c.UpgradeEnabled, c.UpgradeGroupIDs, c.SuccessesPerStep = true, []int64{retained.ID}, 1
			data, err := json.Marshal(c)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, string(data))
			require.NoError(t, err)
			a := degradationAccountFixture(t, 20, nil, []string{"incorrect"}, time.Minute)
			require.NoError(t, repo.BindGroups(ctx, a.ID, []int64{selected.ID, retained.ID}))
			require.NoError(t, repo.SetGroupAllowedModels(ctx, a.ID, map[int64][]string{retained.ID: {"gpt-6.1-sol"}}))
			var groupEventsBefore int
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type=$2", a.ID, service.SchedulerOutboxEventAccountGroupsChanged).Scan(&groupEventsBefore))
			require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
			got, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.True(t, got.HasOAuthDegradationAction())
			wantConcurrency := 20
			if concurrency {
				wantConcurrency = 10
			}
			require.Equal(t, wantConcurrency, got.Concurrency)
			require.Equal(t, !models, got.IsModelSupported("gpt-6.1-sol"))
			require.True(t, got.IsModelSupported("gpt-6-luna"))
			var hold bool
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE((extra->'auto_config_concurrency'->>'degradation_hold')::boolean,false) FROM accounts WHERE id=$1", a.ID).Scan(&hold))
			require.Equal(t, concurrency, hold, "only concurrency action pauses the success ramp")
			if groups {
				require.Equal(t, []int64{retained.ID}, got.GroupIDs)
				data, err = json.Marshal(got.Extra[service.OAuthDegradationActionExtraKey])
				require.NoError(t, err)
				var state service.OAuthDegradationState
				require.NoError(t, json.Unmarshal(data, &state))
				require.ElementsMatch(t, []int64{selected.ID, absent.ID}, state.GroupIDs)
				require.Equal(t, []int64{selected.ID}, state.RemovedGroupIDs)
				var groupEventsAfter int
				require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type=$2", a.ID, service.SchedulerOutboxEventAccountGroupsChanged).Scan(&groupEventsAfter))
				require.Equal(t, groupEventsBefore+1, groupEventsAfter, "group removal publishes its own durable outbox event")
				var changed int
				require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type=$2 AND payload->'group_ids' @> to_jsonb(ARRAY[$3::bigint])", a.ID, service.SchedulerOutboxEventAccountGroupsChanged, selected.ID).Scan(&changed))
				require.Positive(t, changed, "removed group bucket must receive invalidation metadata")
				// A stale editor and a newly-added previously absent selection must
				// both honor the durable closed scope, preserving unrelated groups.
				require.NoError(t, repo.BindGroups(ctx, a.ID, []int64{selected.ID, retained.ID, absent.ID}))
				got, err = repo.GetByID(ctx, a.ID)
				require.NoError(t, err)
				require.Equal(t, []int64{retained.ID}, got.GroupIDs)
			} else {
				require.ElementsMatch(t, []int64{selected.ID, retained.ID}, got.GroupIDs)
			}
			require.Equal(t, []string{"gpt-6.1-sol"}, got.GroupAllowedModels(retained.ID), "remaining group-specific models stay intact")
			// Ordinary and bulk editors must follow the selected action rather
			// than treating any degradation marker as a concurrency cap.
			got.Concurrency = 30
			require.NoError(t, repo.Update(ctx, got))
			proposed := 40
			_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{Concurrency: &proposed})
			require.NoError(t, err)
			got, err = repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			if concurrency {
				require.Equal(t, 10, got.Concurrency)
			} else {
				require.Equal(t, 40, got.Concurrency)
			}
			require.NoError(t, repo.RecordConcurrencyResult(ctx, service.AccountConcurrencyResult{AccountID: a.ID, StartedAt: time.Now(), Success: true}, c))
			got, err = repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			if concurrency {
				require.Equal(t, 10, got.Concurrency)
			} else {
				require.Equal(t, 41, got.Concurrency, "model/group action must leave native success upgrades working")
			}
			require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
			var events int
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'", a.ID).Scan(&events))
			require.Equal(t, 1, events, "selected actions retain one-time transaction semantics")
			var status string
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT status FROM groups WHERE id=$1", selected.ID).Scan(&status))
			require.Equal(t, service.StatusActive, status, "closing account membership must not disable the group globally")
		})
	}
}

func TestOAuthDegradationGroupOnlyAdmissionKeepsIndependentConcurrency(t *testing.T) {
	f := newAdmissionFixture(t)
	state := service.OAuthDegradationState{LimitConcurrency: degradationActionFlag(false), LimitModels: degradationActionFlag(false), LimitGroups: true, GroupIDs: []int64{f.policy.IsolationGroupID}}
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(t.Context(), `UPDATE accounts SET extra=jsonb_set(extra,'{oauth_degradation_action}',$2::jsonb) WHERE id=$1`, f.account.ID, string(raw))
	require.NoError(t, err)
	for range 5 {
		require.Equal(t, "observed", f.round("passed"))
	}
	require.Equal(t, "admission_passed", f.round("passed"))
	var concurrency int
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT concurrency FROM accounts WHERE id=$1", f.account.ID).Scan(&concurrency))
	require.Equal(t, f.policy.PassedConcurrency, concurrency, "group-only action does not cap 2FA admission concurrency")
	for range 5 {
		require.Equal(t, "observed", f.round("answer_29"))
	}
	require.Equal(t, "admission_isolation_pending", f.round("answer_29"))
	require.Equal(t, "isolation_pending", f.state().Phase)
	var schedulable bool
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT schedulable FROM accounts WHERE id=$1", f.account.ID).Scan(&schedulable))
	require.False(t, schedulable, "conflicting automated isolation must keep traffic paused without restoring a closed group")
	var bound bool
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT EXISTS(SELECT 1 FROM account_groups WHERE account_id=$1 AND group_id=$2)", f.account.ID, f.policy.IsolationGroupID).Scan(&bound))
	require.False(t, bound)
}
