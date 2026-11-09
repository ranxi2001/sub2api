//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRolling24HoursHalfOpenBoundaries(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: "rolling-window@example.test"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "rolling-window-fixture", Name: "window"})
	account := mustCreateAccount(t, client, &service.Account{Name: "rolling-window"})
	end, err := time.Parse(time.RFC3339Nano, "2026-10-01T00:00:00.123+09:00")
	require.NoError(t, err)
	start := end.Add(-24 * time.Hour)
	require.Equal(t, 24*time.Hour, end.Sub(start))
	accountStatsCost, multiplier := 5.0, 2.0
	// PostgreSQL timestamps have microsecond precision. Only start and end-1us belong.
	for _, instant := range []time.Time{start.Add(-time.Microsecond), start, end.Add(-time.Microsecond), end, end.Add(time.Microsecond)} {
		_, err := repo.Create(ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID,
			Model: "window-fixture", InputTokens: 1, OutputTokens: 2,
			TotalCost: 7, ActualCost: 3, AccountStatsCost: &accountStatsCost, AccountRateMultiplier: &multiplier,
			CreatedAt: instant,
		})
		require.NoError(t, err)
	}
	filters := usagestats.UsageLogFilters{UserID: user.ID, StartTime: &start, EndTime: &end, ExactTotal: true}
	logs, page, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 10, SortBy: "created_at", SortOrder: "asc"}, filters)
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Len(t, logs, 2)
	require.True(t, logs[0].CreatedAt.Equal(start), "lower boundary is inclusive")
	require.True(t, logs[1].CreatedAt.Equal(end.Add(-time.Microsecond)), "upper boundary is exclusive")
	stats, err := repo.GetStatsWithFilters(ctx, filters)
	require.NoError(t, err)
	require.EqualValues(t, 2, stats.TotalRequests)
	require.EqualValues(t, 6, stats.TotalTokens)
	require.InDelta(t, 14, stats.TotalCost, 1e-9, "standard cost")
	require.InDelta(t, 6, stats.TotalActualCost, 1e-9, "user consumption")
	require.NotNil(t, stats.TotalAccountCost)
	require.InDelta(t, 20, *stats.TotalAccountCost, 1e-9, "account cost")
	for _, granularity := range []string{"hour", "day"} {
		trend, err := repo.GetUsageTrendWithUsageFilters(ctx, start, end, granularity, filters)
		require.NoError(t, err)
		var requests int64
		var cost, actualCost float64
		for _, bucket := range trend {
			requests += bucket.Requests
			cost += bucket.Cost
			actualCost += bucket.ActualCost
		}
		require.EqualValues(t, 2, requests, granularity)
		require.InDelta(t, 14, cost, 1e-9, granularity)
		require.InDelta(t, 6, actualCost, 1e-9, granularity)
	}
}

// Populate aggregates through the production bucket writers, then check the
// unfiltered read path against raw logs, including dates returned by PostgreSQL.
func TestUsageTrendAggregateTimezoneRealBuckets(t *testing.T) {
	for _, tc := range []struct{ name, zone, start, end, granularity string }{
		{"utc_day_from_negative_offset", "UTC", "2026-09-29T20:00:00-04:00", "2026-09-30T20:00:00-04:00", "day"},
		{"shanghai_utc_midnight", "Asia/Shanghai", "2026-09-30T00:00:00Z", "2026-10-01T00:00:00Z", "day"},
		{"half_hour_utc_hour", "Asia/Kolkata", "2026-09-30T00:00:00Z", "2026-10-01T00:00:00Z", "hour"},
		{"quarter_hour_utc_hour", "Asia/Kathmandu", "2026-09-30T00:00:00Z", "2026-10-01T00:00:00Z", "hour"},
		{"dst_spring_utc_day", "America/New_York", "2026-03-08T00:00:00Z", "2026-03-09T00:00:00Z", "day"},
		{"dst_fall_utc_day", "America/New_York", "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z", "day"},
		{"dst_spring_23h_day", "America/New_York", "2026-03-08T00:00:00-05:00", "2026-03-09T00:00:00-04:00", "day"},
		{"dst_fall_25h_day", "America/New_York", "2026-11-01T00:00:00-04:00", "2026-11-02T00:00:00-05:00", "day"},
		{"dst_repeated_hour", "America/New_York", "2026-11-01T01:00:00-04:00", "2026-11-01T02:00:00-05:00", "hour"},
		{"dst_half_hour_shift", "Australia/Lord_Howe", "2026-10-03T00:00:00Z", "2026-10-05T00:00:00Z", "hour"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tz := useGroupUsageRepositoryTestTimezone(t, tc.zone)
			ctx := context.Background()
			tx := testEntTx(t)
			client := tx.Client()
			// Each case is a rolled-back fixture transaction. Isolate the unfiltered
			// query from fixtures left by other tests, without touching external data.
			_, err := tx.ExecContext(ctx, "DELETE FROM usage_logs")
			require.NoError(t, err)
			for _, table := range []string{"usage_dashboard_hourly", "usage_dashboard_daily"} {
				_, err = tx.ExecContext(ctx, "DELETE FROM "+table)
				require.NoError(t, err)
			}
			_, err = tx.ExecContext(ctx, "SELECT set_config('TimeZone', $1, true)", tc.zone)
			require.NoError(t, err)
			repo := newUsageLogRepositoryWithSQL(client, tx)
			repo.configuredTimezone = tz
			user := mustCreateUser(t, client, &service.User{Email: "local-buckets@example.test"})
			key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "local-buckets-fixture", Name: "buckets"})
			account := mustCreateAccount(t, client, &service.Account{Name: "local-buckets"})
			start, err := time.Parse(time.RFC3339Nano, tc.start)
			require.NoError(t, err)
			end, err := time.Parse(time.RFC3339Nano, tc.end)
			require.NoError(t, err)
			format := "2006-01-02"
			if tc.granularity == "hour" {
				format = "2006-01-02 15:00"
			}
			wantRequests := map[string]int64{}
			accountStatsCost, multiplier := 5.0, 2.0
			for _, instant := range []time.Time{start.Add(-time.Microsecond), start, start.Add(30 * time.Minute), end.Add(-time.Microsecond), end, end.Add(time.Microsecond)} {
				_, err = repo.Create(ctx, &service.UsageLog{
					UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID,
					Model: "local-buckets-fixture", InputTokens: 1, OutputTokens: 2,
					TotalCost: 7, ActualCost: 3, AccountStatsCost: &accountStatsCost, AccountRateMultiplier: &multiplier,
					CreatedAt: instant,
				})
				require.NoError(t, err)
				if !instant.Before(start) && instant.Before(end) {
					wantRequests[instant.In(tz.timezoneLocation()).Format(format)]++
				}
			}
			writer := newDashboardAggregationRepositoryWithSQL(tx)
			writer.configuredTimezone = tz
			// A wide interval gives the real writers all boundary records, including
			// records just outside the requested window in the same local bucket.
			aggregateStart, aggregateEnd := start.Add(-48*time.Hour), end.Add(48*time.Hour)
			require.NoError(t, writer.upsertHourlyAggregates(ctx, aggregateStart, aggregateEnd))
			require.NoError(t, writer.upsertDailyAggregates(ctx, aggregateStart, aggregateEnd))
			aggregated, err := repo.getUsageTrendFromAggregates(ctx, start, end, tc.granularity)
			require.NoError(t, err)
			require.NotEmpty(t, aggregated, "regression must exercise populated real aggregate buckets")
			trend, err := repo.GetUsageTrendWithUsageFilters(ctx, start, end, tc.granularity, usagestats.UsageLogFilters{})
			require.NoError(t, err)
			gotRequests := map[string]int64{}
			var requests int64
			var cost, actualCost float64
			for _, point := range trend {
				gotRequests[point.Date] += point.Requests
				requests += point.Requests
				cost += point.Cost
				actualCost += point.ActualCost
			}
			require.Equal(t, wantRequests, gotRequests, "date labels and per-bucket membership must match raw log semantics")
			require.EqualValues(t, 3, requests)
			require.InDelta(t, 21, cost, 1e-9)
			require.InDelta(t, 9, actualCost, 1e-9)
			stats, err := repo.GetStatsWithFilters(ctx, usagestats.UsageLogFilters{StartTime: &start, EndTime: &end})
			require.NoError(t, err)
			require.EqualValues(t, requests, stats.TotalRequests)
			require.InDelta(t, cost, stats.TotalCost, 1e-9)
			require.InDelta(t, actualCost, stats.TotalActualCost, 1e-9)
			require.NotNil(t, stats.TotalAccountCost)
			require.InDelta(t, 30, *stats.TotalAccountCost, 1e-9)
		})
	}
}
