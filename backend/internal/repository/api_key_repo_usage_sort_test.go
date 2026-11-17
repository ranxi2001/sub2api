package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyUsageSortPostgresQuery(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	builder := entsql.Dialect(dialect.Postgres)
	keys := builder.Table(apikey.Table)
	query := builder.Select(keys.C(apikey.FieldID)).From(keys).
		Where(entsql.EQ(keys.C(apikey.FieldUserID), int64(42))).Limit(2).Offset(2)
	for _, order := range apiKeyUsageOrder(start, end, "desc") {
		order(query)
	}
	sql, args := query.Query()
	require.Equal(t, []any{int64(42), start, end}, args)
	require.NotContains(t, sql, "`")
	require.Contains(t, sql, `"usage_logs"."created_at" >= $2 AND "usage_logs"."created_at" < $3`)
	require.Contains(t, sql, `), 0) DESC, "api_keys"."id" DESC LIMIT 2 OFFSET 2`)
}

func TestAPIKeyRepositoryListByUserIDSortByUsage(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "usage-sort@example.com")
	other := mustCreateAPIKeyRepoUser(t, ctx, client, "other-usage-sort@example.com")
	accountID := mustCreateAPIKeyRepoAccount(t, ctx, client, "usage-sort")
	group, err := client.Group.Create().SetName("Example").Save(ctx)
	require.NoError(t, err)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	createKey := func(userID int64, name, status string, groupID *int64) int64 {
		key := &service.APIKey{UserID: userID, Key: "sk-" + name, Name: name, Status: status, GroupID: groupID}
		require.NoError(t, repo.Create(ctx, key))
		return key.ID
	}
	addUsage := func(keyID int64, cost float64, at time.Time) {
		_, err := client.UsageLog.Create().SetUserID(user.ID).SetAPIKeyID(keyID).
			SetAccountID(accountID).SetRequestID(fmt.Sprintf("usage-%d-%s", keyID, at.Format(time.RFC3339Nano))).
			SetModel("test-model").SetActualCost(cost).SetTotalCost(1000 - cost).SetCreatedAt(at).Save(ctx)
		require.NoError(t, err)
	}
	low := createKey(user.ID, "match-low", service.StatusActive, &group.ID)
	high := createKey(user.ID, "match-high", service.StatusActive, &group.ID)
	tied := createKey(user.ID, "match-tied", service.StatusActive, &group.ID)
	zero := createKey(user.ID, "match-zero", service.StatusActive, nil)
	// The high cost is a sum of requests; neither ID nor per-request cost gives the right order.
	addUsage(low, 3, start)
	addUsage(high, 2, start)
	addUsage(high, 2, start.Add(time.Hour))
	addUsage(tied, 3, start)
	// Both bounds are significant: the inclusive end day is converted to next midnight.
	addUsage(low, 100, start.Add(-time.Second))
	addUsage(low, 100, end)
	addUsage(high, 200, end.Add(time.Hour))
	for _, id := range []int64{
		createKey(other.ID, "match-other", service.StatusActive, &group.ID),
		createKey(user.ID, "excluded-search", service.StatusActive, &group.ID),
		createKey(user.ID, "match-disabled", service.StatusDisabled, &group.ID),
	} {
		addUsage(id, 999, start)
	}
	deleted := createKey(user.ID, "match-deleted", service.StatusActive, &group.ID)
	addUsage(deleted, 999, start)
	require.NoError(t, repo.Delete(ctx, deleted))

	for _, tc := range []struct {
		name, order string
		start, end  time.Time
		groupID     *int64
		want        []int64
	}{
		{"default descending", "", start, end, nil, []int64{high, tied, low, zero}},
		{"descending", "desc", start, end, nil, []int64{high, tied, low, zero}},
		{"ascending", "asc", start, end, nil, []int64{zero, low, tied, high}},
		{"changed range", "desc", end, end.AddDate(0, 0, 1), nil, []int64{high, low, zero, tied}},
		{"group filter", "desc", start, end, &group.ID, []int64{high, tied, low}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []int64
			for page := 1; page <= (len(tc.want)+1)/2; page++ {
				keys, result, err := repo.ListByUserID(ctx, user.ID, pagination.PaginationParams{
					Page: page, PageSize: 2, SortBy: "usage", SortOrder: tc.order,
				}, service.APIKeyListFilters{
					Search: "match", Status: service.StatusActive, GroupID: tc.groupID,
					UsageStartTime: tc.start, UsageEndTime: tc.end,
				})
				require.NoError(t, err)
				require.EqualValues(t, len(tc.want), result.Total)
				for _, key := range keys {
					got = append(got, key.ID)
					if key.GroupID != nil {
						require.NotNil(t, key.Group)
					}
				}
			}
			require.Equal(t, tc.want, got)
		})
	}
}
