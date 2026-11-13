//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// JSON lets this behavioral regression also run against the pre-platform model:
// the old model ignores Platform and must fail on the returned SQL result count.
func TestUsagePlatformHistoricalQuery(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: "platform-fixture@example.invalid"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "synthetic-platform-key", Name: "Fixture"})
	account := mustCreateAccount(t, client, &service.Account{Name: "Fixture", Platform: "openai"})
	group := mustCreateGroup(t, client, &service.Group{Name: "Fixture", Platform: "composite"})
	other := mustCreateGroup(t, client, &service.Group{Name: "Other fixture", Platform: "openai"})
	start := time.Now().UTC().Add(-time.Hour)
	end := start.Add(time.Hour)
	platforms := domain.ConcretePlatformIDs()
	for i, platform := range append(append([]string{}, platforms...), "") {
		for j, groupID := range []int64{group.ID, other.ID} {
			log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID,
				GroupID: &groupID, RequestID: fmt.Sprintf("platform-%d-%d", i, j), Model: "fixture-model",
				InputTokens: 10, OutputTokens: 20, TotalCost: 2, ActualCost: 1, CreatedAt: start.Add(time.Minute)}
			raw, err := json.Marshal(map[string]string{"Platform": platform})
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, log))
			_, err = repo.Create(ctx, log)
			require.NoError(t, err)
		}
	}
	// Neither current entities nor soft deletion may reclassify a stored request.
	_, err := client.Account.UpdateOneID(account.ID).SetPlatform("anthropic").SetDeletedAt(time.Now()).Save(ctx)
	require.NoError(t, err)
	_, err = client.Group.UpdateOneID(group.ID).SetPlatform("gemini").SetDeletedAt(time.Now()).Save(ctx)
	require.NoError(t, err)
	for _, platform := range append(append([]string{}, platforms...), "unknown", "") {
		t.Run(platform, func(t *testing.T) {
			filters := usagestats.UsageLogFilters{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID,
				GroupID: group.ID, Model: "fixture-model", StartTime: &start, EndTime: &end, ExactTotal: true}
			raw, err := json.Marshal(map[string]string{"Platform": platform})
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &filters))
			want := int64(1)
			if platform == "" { want = int64(len(platforms) + 1) }
			rows, page, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 1}, filters)
			require.NoError(t, err)
			require.Equal(t, want, page.Total, "platform predicate must filter historical rows in SQL")
			require.Len(t, rows, 1)
			stats, err := repo.GetStatsWithFilters(ctx, filters)
			require.NoError(t, err)
			require.Equal(t, want, stats.TotalRequests)
			require.Equal(t, float64(want), stats.TotalActualCost)
			require.Equal(t, want*30, stats.TotalTokens)
			// Export uses the same repository query with exact_total and larger pages.
			exported, exportPage, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, filters)
			require.NoError(t, err)
			require.Equal(t, page.Total, exportPage.Total)
			require.Len(t, exported, int(want))
			filters.GroupID = 0
			_, allGroups, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, filters)
			require.NoError(t, err)
			require.Equal(t, want*2, allGroups.Total)
		})
	}
}
