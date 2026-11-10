//go:build integration

package repository

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Every native membership editor may restore a group removed by a past action.
func TestOAuthDegradationGroupBindingWritersRestoreNativeMembership(t *testing.T) {
	for _, writer := range []string{"account form", "single add", "group bulk", "scoped account form"} {
		t.Run(writer, func(t *testing.T) {
			tx := testEntTx(t)
			ctx := dbent.NewTxContext(context.Background(), tx)
			client := tx.Client()
			repo := newAccountRepositoryWithSQL(client, tx, nil)
			closed := mustCreateGroup(t, client, &service.Group{Name: "degradation-closed"})
			kept := mustCreateGroup(t, client, &service.Group{Name: "degradation-kept"})
			account := mustCreateAccount(t, client, &service.Account{
				Name: "degradation-group-writer",
				Extra: map[string]any{service.OAuthDegradationActionExtraKey: map[string]any{
					"models": []string{}, "limit_concurrency": false, "limit_models": false,
					"limit_groups": true, "group_ids": []int64{closed.ID},
				}},
			})
			require.NoError(t, repo.BindGroups(ctx, account.ID, []int64{kept.ID}))
			require.NoError(t, repo.SetGroupAllowedModels(ctx, account.ID, map[int64][]string{kept.ID: {"gpt-6-luna"}}))
			switch writer {
			case "account form":
				require.NoError(t, repo.BindGroups(ctx, account.ID, []int64{closed.ID, kept.ID}))
			case "single add":
				require.NoError(t, repo.AddToGroup(ctx, account.ID, closed.ID, 1))
			case "group bulk":
				groups := newGroupRepositoryWithSQL(client, tx)
				require.NoError(t, groups.BindAccountsToGroup(ctx, closed.ID, []int64{account.ID}))
			case "scoped account form":
				require.NoError(t, repo.scopedAdminBindGroups(ctx, account.ID, []int64{closed.ID, kept.ID}))
			}
			after, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			require.ElementsMatch(t, []int64{closed.ID, kept.ID}, after.GroupIDs)
			require.Equal(t, []string{"gpt-6-luna"}, after.GroupAllowedModels(kept.ID))
			require.True(t, after.HasOAuthDegradationAction())
		})
	}
}

func TestOAuthDegradationLegacyMarkerDoesNotCloseGroups(t *testing.T) {
	tx := testEntTx(t)
	ctx := dbent.NewTxContext(context.Background(), tx)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	group := mustCreateGroup(t, client, &service.Group{Name: "degradation-legacy-group"})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "degradation-legacy-membership",
		Extra: map[string]any{service.OAuthDegradationActionExtraKey: map[string]any{
			"models": []string{"gpt-6-luna"}, "concurrency_cap": 10,
		}},
	})
	require.NoError(t, repo.BindGroups(ctx, account.ID, []int64{group.ID}))
	after, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{group.ID}, after.GroupIDs)
}

func TestOAuthDegradationAuditDoesNotOverrideNativeExcel403Destination(t *testing.T) {
	tx := testEntTx(t)
	ctx := dbent.NewTxContext(context.Background(), tx)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	kept := mustCreateGroup(t, client, &service.Group{Name: "degradation-bps-kept", Platform: service.PlatformOpenAI})
	closed := mustCreateGroup(t, client, &service.Group{Name: "degradation-bps-closed", Platform: service.PlatformOpenAI})
	input := newExcelBPSAutoDisableAccount()
	input.Extra[service.ExcelBPSAutoMoveOn403Key] = true
	input.Extra[service.ExcelBPS403TargetGroupIDKey] = closed.ID
	input.Extra[service.OAuthDegradationActionExtraKey] = map[string]any{
		"models": []string{}, "limit_concurrency": false, "limit_models": false,
		"limit_groups": true, "group_ids": []int64{closed.ID},
	}
	account := mustCreateAccount(t, client, input)
	require.NoError(t, repo.BindGroups(ctx, account.ID, []int64{kept.ID}))
	before, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	changed, err := repo.MoveExcelBPSOn403(ctx, before)
	require.NoError(t, err)
	require.True(t, changed)
	after, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{closed.ID}, after.GroupIDs)
	require.True(t, after.HasOAuthDegradationAction(), "native routing changes retain historical action evidence")
}
