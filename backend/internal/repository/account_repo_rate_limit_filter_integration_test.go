//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRateLimitedFilterPaginationAndScope(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	group := mustCreateGroup(t, client, &service.Group{Name: "rate-limit-visible", Platform: service.PlatformOpenAI})
	hiddenGroup := mustCreateGroup(t, client, &service.Group{Name: "rate-limit-hidden", Platform: service.PlatformOpenAI})
	future := time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour)
	past := future.Add(-2 * time.Hour)
	create := func(name string, change func(*dbent.AccountCreate)) *dbent.Account {
		builder := client.Account.Create().SetName(name).
			SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeOAuth).
			SetStatus(service.StatusActive).SetSchedulable(true).
			SetExtra(map[string]any{"privacy_mode": service.PrivacyModeTrainingOff}).
			SetRateLimitResetAt(future)
		if change != nil {
			change(builder)
		}
		row, err := builder.Save(ctx)
		require.NoError(t, err)
		groupID := group.ID
		if name == "match-hidden" {
			groupID = hiddenGroup.ID
		}
		mustBindAccountToGroup(t, client, row.ID, groupID, 1)
		return row
	}

	errorRow := create("match-error", func(b *dbent.AccountCreate) { b.SetStatus(service.StatusError).SetErrorMessage("fixture error") })
	tempRow := create("match-temp", func(b *dbent.AccountCreate) { b.SetTempUnschedulableUntil(future) })
	disabledRow := create("match-disabled", func(b *dbent.AccountCreate) { b.SetStatus(service.StatusDisabled).SetSchedulable(false) })
	create("match-expired", func(b *dbent.AccountCreate) { b.SetRateLimitResetAt(past) })
	noLimit := create("match-no-limit", nil)
	err := client.Account.UpdateOneID(noLimit.ID).ClearRateLimitResetAt().Exec(ctx)
	require.NoError(t, err)
	create("match-platform", func(b *dbent.AccountCreate) { b.SetPlatform(service.PlatformAnthropic) })
	create("match-type", func(b *dbent.AccountCreate) { b.SetType(service.AccountTypeAPIKey) })
	create("other-name", nil)
	create("match-privacy", func(b *dbent.AccountCreate) { b.SetExtra(map[string]any{"privacy_mode": service.PrivacyModeFailed}) })
	create("match-deleted", func(b *dbent.AccountCreate) { b.SetDeletedAt(time.Now()) })
	create("match-hidden", nil)
	want := []int64{disabledRow.ID, errorRow.ID, tempRow.ID}
	for _, status := range []string{"rate_limited", "error, rate_limited"} {
		for _, scoped := range []struct {
			name    string
			ctx     context.Context
			groupID int64
		}{
			{"group", ctx, group.ID},
			{"observer", service.WithObserverScope(ctx, []int64{group.ID}), 0},
		} {
			t.Run(status+"/"+scoped.name, func(t *testing.T) {
				var ids []int64
				for page := 1; page <= len(want)+1; page++ {
					rows, result, err := repo.ListWithFilters(scoped.ctx,
						pagination.PaginationParams{Page: page, PageSize: 1, SortBy: "name", SortOrder: "asc"},
						service.PlatformOpenAI, service.AccountTypeOAuth, status, "match-", scoped.groupID, service.PrivacyModeTrainingOff)
					require.NoError(t, err)
					require.Equal(t, int64(len(want)), result.Total)
					if page > len(want) {
						require.Empty(t, rows)
						continue
					}
					require.Len(t, rows, 1)
					ids = append(ids, rows[0].ID)
				}
				require.Equal(t, want, ids)
				rows, err := repo.ListAllWithFilters(scoped.ctx, service.PlatformOpenAI, service.AccountTypeOAuth, status, "match-", scoped.groupID, service.PrivacyModeTrainingOff)
				require.NoError(t, err)
				require.Len(t, rows, len(want), "bulk/export queries must use the same scope")
			})
		}
	}
	for _, original := range []*dbent.Account{errorRow, tempRow, disabledRow} {
		row, err := client.Account.Get(ctx, original.ID)
		require.NoError(t, err)
		require.Equal(t, original.Status, row.Status)
		require.Equal(t, original.ErrorMessage, row.ErrorMessage)
		require.Equal(t, original.Schedulable, row.Schedulable)
		require.Equal(t, original.RateLimitResetAt, row.RateLimitResetAt)
		require.Equal(t, original.TempUnschedulableUntil, row.TempUnschedulableUntil)
	}
}
