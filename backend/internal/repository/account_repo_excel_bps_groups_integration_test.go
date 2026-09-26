//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func bpsGroupEventCount(t *testing.T, ctx context.Context, client *dbent.Client, accountID int64) int {
	t.Helper()
	var count int
	require.NoError(t, scanSingleRow(ctx, client,
		"SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1 AND event_type = $2",
		[]any{accountID, service.SchedulerOutboxEventAccountGroupsChanged}, &count))
	return count
}

func TestMoveExcelBPSOn403Memberships(t *testing.T) {
	for _, mode := range []string{"new destination", "existing destination", "leave all", "ungrouped to destination", "already at destination", "already ungrouped"} {
		t.Run(mode, func(t *testing.T) {
			tx := testEntTx(t)
			ctx := dbent.NewTxContext(context.Background(), tx)
			client := tx.Client()
			repo := newAccountRepositoryWithSQL(client, tx, nil)
			oldGroup := mustCreateGroup(t, client, &service.Group{Name: "bps-old", Platform: service.PlatformOpenAI})
			destination := mustCreateGroup(t, client, &service.Group{Name: "bps-destination", Platform: service.PlatformComposite})
			target := destination.ID
			initial, want := []int64{oldGroup.ID}, []int64{destination.ID}
			wantChange := true
			switch mode {
			case "existing destination":
				initial = append(initial, destination.ID)
			case "leave all":
				target, want = 0, nil
			case "ungrouped to destination":
				initial = nil
			case "already at destination":
				initial, wantChange = want, false
			case "already ungrouped":
				target, initial, want, wantChange = 0, nil, nil, false
			}
			input := newExcelBPSAutoDisableAccount()
			input.Extra[service.ExcelBPSAutoMoveOn403Key] = true
			input.Extra[service.ExcelBPS403TargetGroupIDKey] = target
			account := mustCreateAccount(t, client, input)
			require.NoError(t, repo.BindGroups(ctx, account.ID, initial))
			if mode == "existing destination" {
				require.NoError(t, repo.SetGroupAllowedModels(ctx, account.ID, map[int64][]string{target: {"gpt-6-astra"}}))
				_, err := client.ExecContext(ctx, "UPDATE account_groups SET priority = 17 WHERE account_id = $1 AND group_id = $2", account.ID, target)
				require.NoError(t, err)
			}
			before, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			baseline := bpsGroupEventCount(t, ctx, client, account.ID)
			changed, err := repo.MoveExcelBPSOn403(ctx, before)
			require.NoError(t, err)
			require.Equal(t, wantChange, changed)
			after, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			require.ElementsMatch(t, want, after.GroupIDs)
			require.ElementsMatch(t, initial, before.GroupIDs, "request snapshots must remain immutable")
			afterExtra := maps.Clone(after.Extra)
			if wantChange {
				requireExcelBPS403MoveRecord(t, after.Extra, target)
				delete(afterExtra, service.ExcelBPS403MovedAtKey)
				delete(afterExtra, service.ExcelBPS403MovedGroupIDKey)
			}
			require.Equal(t, before.Extra, afterExtra, "only the group-action record is added")
			require.Equal(t, before.Credentials, after.Credentials)
			require.Equal(t, before.Status, after.Status)
			require.Equal(t, before.Schedulable, after.Schedulable)
			if mode == "existing destination" {
				require.Len(t, after.AccountGroups, 1)
				require.Equal(t, 17, after.AccountGroups[0].Priority)
				require.Equal(t, []string{"gpt-6-astra"}, after.AccountGroups[0].AllowedModels)
			}
			if wantChange {
				require.Equal(t, baseline+1, bpsGroupEventCount(t, ctx, client, account.ID))
				var payload []byte
				require.NoError(t, scanSingleRow(ctx, client, "SELECT payload FROM scheduler_outbox WHERE account_id = $1 AND event_type = $2 ORDER BY id DESC LIMIT 1",
					[]any{account.ID, service.SchedulerOutboxEventAccountGroupsChanged}, &payload))
				var event map[string][]int64
				require.NoError(t, json.Unmarshal(payload, &event))
				require.ElementsMatch(t, mergeGroupIDs(initial, want), event["group_ids"], "refresh source and destination buckets, including leave-all")
			} else {
				require.Equal(t, baseline, bpsGroupEventCount(t, ctx, client, account.ID))
			}
			changed, err = repo.MoveExcelBPSOn403(ctx, before)
			require.NoError(t, err)
			require.False(t, changed)
			// Both user options can run against the same immutable request snapshot.
			changed, err = repo.DisableExcelBPSOn403(ctx, before)
			require.NoError(t, err)
			require.True(t, changed)
			after, err = repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			require.False(t, after.IsExcelBPSEnabled())
			require.ElementsMatch(t, want, after.GroupIDs)
			require.Contains(t, after.Extra, service.ExcelBPS403DisabledAtKey)
			if wantChange {
				requireExcelBPS403MoveRecord(t, after.Extra, target)
			}
		})
	}
}

func requireExcelBPS403MoveRecord(t *testing.T, extra map[string]any, target int64) {
	t.Helper()
	at, ok := extra[service.ExcelBPS403MovedAtKey].(string)
	require.True(t, ok, "the automatic group action records when it happened")
	parsed, err := time.Parse(time.RFC3339, at)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), parsed, time.Minute)
	group, err := json.Marshal(extra[service.ExcelBPS403MovedGroupIDKey])
	require.NoError(t, err)
	require.Equal(t, strconv.FormatInt(target, 10), string(group))
}

func TestExcelBPS403MoveRecordSurvivesAccountEdits(t *testing.T) {
	tx := testEntTx(t)
	ctx := dbent.NewTxContext(context.Background(), tx)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	oldGroup := mustCreateGroup(t, client, &service.Group{Name: "bps-record-old", Platform: service.PlatformOpenAI})
	destination := mustCreateGroup(t, client, &service.Group{Name: "bps-record-target", Platform: service.PlatformOpenAI})
	input := newExcelBPSAutoDisableAccount()
	input.Extra[service.ExcelBPSAutoMoveOn403Key] = true
	input.Extra[service.ExcelBPS403TargetGroupIDKey] = destination.ID
	account := mustCreateAccount(t, client, input)
	require.NoError(t, repo.BindGroups(ctx, account.ID, []int64{oldGroup.ID}))
	load := func() *service.Account {
		t.Helper()
		loaded, err := repo.GetByID(ctx, account.ID)
		require.NoError(t, err)
		return loaded
	}
	changed, err := repo.MoveExcelBPSOn403(ctx, load())
	require.NoError(t, err)
	require.True(t, changed)
	record := load().Extra
	requireExcelBPS403MoveRecord(t, record, destination.ID)

	// An ordinary edit keeps the record, even when it echoes other values.
	edited := load()
	edited.Name = "bps-record-renamed"
	edited.Extra[service.ExcelBPS403MovedAtKey] = "2000-01-01T00:00:00Z"
	edited.Extra[service.ExcelBPS403MovedGroupIDKey] = 0
	require.NoError(t, repo.Update(ctx, edited))
	after := load()
	require.Equal(t, "bps-record-renamed", after.Name)
	require.Equal(t, record[service.ExcelBPS403MovedAtKey], after.Extra[service.ExcelBPS403MovedAtKey])
	require.Equal(t, record[service.ExcelBPS403MovedGroupIDKey], after.Extra[service.ExcelBPS403MovedGroupIDKey])

	// A repeated 403 after the move changes nothing and keeps the first record.
	changed, err = repo.MoveExcelBPSOn403(ctx, after)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, record[service.ExcelBPS403MovedAtKey], load().Extra[service.ExcelBPS403MovedAtKey])
}

func TestMoveExcelBPSOn403HonorsCurrentSettings(t *testing.T) {
	for _, kind := range []string{"opt-in withdrawn", "protocol disabled", "target changed", "target missing", "credentials replaced", "groups edited", "account deleted", "type changed", "destination deleted", "destination incompatible"} {
		t.Run(kind, func(t *testing.T) {
			tx := testEntTx(t)
			ctx := dbent.NewTxContext(context.Background(), tx)
			client := tx.Client()
			repo := newAccountRepositoryWithSQL(client, tx, nil)
			oldGroup := mustCreateGroup(t, client, &service.Group{Name: "bps-current", Platform: service.PlatformOpenAI})
			destination := mustCreateGroup(t, client, &service.Group{Name: "bps-target", Platform: service.PlatformOpenAI})
			input := newExcelBPSAutoDisableAccount()
			input.Extra[service.ExcelBPSAutoMoveOn403Key] = true
			input.Extra[service.ExcelBPS403TargetGroupIDKey] = destination.ID
			account := mustCreateAccount(t, client, input)
			require.NoError(t, repo.BindGroups(ctx, account.ID, []int64{oldGroup.ID}))
			before, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			queries := map[string]string{
				"opt-in withdrawn":         "UPDATE accounts SET extra = extra || '{\"openai_excel_bps_auto_move_on_403\":false}' WHERE id = $1",
				"protocol disabled":        "UPDATE accounts SET extra = extra || '{\"openai_excel_bps\":false}' WHERE id = $1",
				"target changed":           "UPDATE accounts SET extra = extra || '{\"openai_excel_bps_403_target_group_id\":0}' WHERE id = $1",
				"target missing":           "UPDATE accounts SET extra = extra - 'openai_excel_bps_403_target_group_id' WHERE id = $1",
				"credentials replaced":     "UPDATE accounts SET credentials = '{\"access_token\":\"new-token\"}' WHERE id = $1",
				"groups edited":            "DELETE FROM account_groups WHERE account_id = $1",
				"account deleted":          "UPDATE accounts SET deleted_at = NOW() WHERE id = $1",
				"type changed":             "UPDATE accounts SET type = 'apikey' WHERE id = $1",
				"destination deleted":      "UPDATE groups SET deleted_at = NOW() WHERE id = $1",
				"destination incompatible": "UPDATE groups SET platform = 'anthropic' WHERE id = $1",
			}
			id := account.ID
			if kind == "destination deleted" || kind == "destination incompatible" {
				id = destination.ID
			}
			_, err = client.ExecContext(ctx, queries[kind], id)
			require.NoError(t, err)
			baseline := bpsGroupEventCount(t, ctx, client, account.ID)
			changed, err := repo.MoveExcelBPSOn403(ctx, before)
			if kind == "destination deleted" || kind == "destination incompatible" {
				require.ErrorIs(t, err, service.ErrGroupNotFound)
			} else {
				require.NoError(t, err)
			}
			require.False(t, changed)
			require.Equal(t, baseline, bpsGroupEventCount(t, ctx, client, account.ID))
			var recorded bool
			require.NoError(t, scanSingleRow(ctx, client, "SELECT extra -> 'openai_excel_bps_403_moved_at' IS NOT NULL FROM accounts WHERE id = $1", []any{account.ID}, &recorded))
			require.False(t, recorded, "a skipped action leaves no record")
			var count int
			require.NoError(t, scanSingleRow(ctx, client, "SELECT COUNT(*) FROM account_groups WHERE account_id = $1 AND group_id = $2", []any{account.ID, oldGroup.ID}, &count))
			if kind == "groups edited" {
				require.Zero(t, count)
			} else {
				require.Equal(t, 1, count)
			}
		})
	}
}

func TestMoveExcelBPSOn403ConcurrentAndCache(t *testing.T) {
	for _, leaveAll := range []bool{false, true} {
		t.Run(fmt.Sprintf("leave_all_%t", leaveAll), func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			oldGroup := mustCreateGroup(t, client, &service.Group{Name: "bps-old", Platform: service.PlatformOpenAI})
			destination := mustCreateGroup(t, client, &service.Group{Name: "bps-next", Platform: service.PlatformOpenAI})
			input := newExcelBPSAutoDisableAccount()
			target := destination.ID
			want := []int64{target}
			if leaveAll {
				target, want = 0, nil
			}
			input.Extra[service.ExcelBPSAutoMoveOn403Key] = true
			input.Extra[service.ExcelBPS403TargetGroupIDKey] = target
			account := mustCreateAccount(t, client, input)
			t.Cleanup(func() {
				_, err := integrationDB.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE account_id = $1", account.ID)
				require.NoError(t, err)
				require.NoError(t, client.Account.DeleteOneID(account.ID).Exec(ctx))
				require.NoError(t, client.Group.DeleteOneID(oldGroup.ID).Exec(ctx))
				require.NoError(t, client.Group.DeleteOneID(destination.ID).Exec(ctx))
			})
			repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
			require.NoError(t, repo.BindGroups(ctx, account.ID, []int64{oldGroup.ID}))
			before, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			baseline := bpsGroupEventCount(t, ctx, client, account.ID)
			cache := &schedulerCacheRecorder{}
			repo = newAccountRepositoryWithSQL(client, integrationDB, cache)
			type outcome struct {
				changed bool
				err     error
			}
			const requests = 16
			results := make(chan outcome, requests)
			start := make(chan struct{})
			for range requests {
				go func() { <-start; changed, err := repo.MoveExcelBPSOn403(ctx, before); results <- outcome{changed, err} }()
			}
			close(start)
			changedCount := 0
			for range requests {
				result := <-results
				require.NoError(t, result.err)
				if result.changed {
					changedCount++
				}
			}
			require.Equal(t, 1, changedCount)
			require.Equal(t, baseline+1, bpsGroupEventCount(t, ctx, client, account.ID))
			after, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			require.ElementsMatch(t, want, after.GroupIDs)
			requireExcelBPS403MoveRecord(t, after.Extra, target)
			require.Len(t, cache.setAccounts, 1)
			require.ElementsMatch(t, want, cache.setAccounts[0].GroupIDs)
			require.True(t, after.IsExcelBPSEnabled(), "independent from auto-disable")
			require.Equal(t, []int64{oldGroup.ID}, before.GroupIDs)
		})
	}
}
