package repository

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestUsageTrendRolling24HoursPreservesQueryBounds(t *testing.T) {
	tz := useGroupUsageRepositoryTestTimezone(t, "UTC")
	for _, tc := range []struct{ name, start, granularity, table string }{
		{"partial_hour", "2026-09-30T00:01:02.123+09:00", "hour", "usage_logs"},
		{"partial_day", "2026-09-30T00:01:02.123+09:00", "day", "usage_logs"},
		{"local_calendar_day", "2026-09-30T00:00:00+09:00", "day", "usage_logs"},
		{"utc_hour_aligned", "2026-09-30T00:00:00Z", "hour", "usage_dashboard_hourly"},
		{"utc_day_aligned", "2026-09-30T00:00:00Z", "day", "usage_dashboard_daily"},
		{"utc_day_from_negative_offset", "2026-09-29T20:00:00-04:00", "day", "usage_dashboard_daily"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var queries []string
			// Record even an unexpected aggregate query: the implementation otherwise
			// swallows that error and falls back, which would conceal this regression.
			matcher := sqlmock.QueryMatcherFunc(func(expected, actual string) error {
				queries = append(queries, actual)
				matched, err := regexp.MatchString(expected, actual)
				if err != nil {
					return err
				}
				if !matched {
					return fmt.Errorf("unexpected query: %s", actual)
				}
				return nil
			})
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			repo := &usageLogRepository{sql: db, configuredTimezone: tz}
			start, err := time.Parse(time.RFC3339Nano, tc.start)
			require.NoError(t, err)
			end := start.Add(24 * time.Hour)
			predicate := `created_at >= \$1 AND created_at < \$2`
			if tc.table == "usage_dashboard_hourly" {
				predicate = `bucket_start >= \$1 AND bucket_start < \$2`
			}
			if tc.table == "usage_dashboard_daily" {
				predicate = `bucket_date >= \$1::date AND bucket_date < \$2::date`
			}
			queryStart, queryEnd := start, end
			if tc.table != "usage_logs" {
				// $1::date must see UTC calendar dates, regardless of the
				// explicit offset used by the caller to express these instants.
				queryStart, queryEnd = start.UTC(), end.UTC()
			}
			mock.ExpectQuery(`(?s)FROM `+tc.table+`\s+WHERE `+predicate).
				WithArgs(queryStart, queryEnd).
				WillReturnRows(sqlmock.NewRows([]string{"date", "requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_tokens", "cost", "actual_cost"}).
					AddRow("2026-09-30", 2, 2, 4, 0, 0, 6, 14, 6))
			trend, err := repo.GetUsageTrendWithUsageFilters(context.Background(), start, end, tc.granularity, usagestats.UsageLogFilters{})
			require.NoError(t, err)
			require.Len(t, queries, 1, "partial buckets must query raw logs directly")
			require.Len(t, trend, 1)
			require.EqualValues(t, 2, trend[0].Requests)
			require.InDelta(t, 14, trend[0].Cost, 1e-9)
			require.InDelta(t, 6, trend[0].ActualCost, 1e-9)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUsageTrendAggregateTimezoneRequiresRawLogs(t *testing.T) {
	for _, tc := range []struct{ name, zone, start, end, granularity string }{
		{"shanghai_utc_midnight", "Asia/Shanghai", "2026-09-30T00:00:00Z", "2026-10-01T00:00:00Z", "day"},
		{"shanghai_local_midnight", "Asia/Shanghai", "2026-09-30T00:00:00+08:00", "2026-10-01T00:00:00+08:00", "day"},
		{"half_hour_utc_hour", "Asia/Kolkata", "2026-09-30T00:00:00Z", "2026-10-01T00:00:00Z", "hour"},
		{"quarter_hour_utc_hour", "Asia/Kathmandu", "2026-09-30T00:00:00Z", "2026-10-01T00:00:00Z", "hour"},
		{"half_hour_local_hour", "Asia/Kolkata", "2026-09-30T00:00:00+05:30", "2026-10-01T00:00:00+05:30", "hour"},
		{"quarter_hour_local_hour", "Asia/Kathmandu", "2026-09-30T00:00:00+05:45", "2026-10-01T00:00:00+05:45", "hour"},
		{"dst_spring_utc_day", "America/New_York", "2026-03-08T00:00:00Z", "2026-03-09T00:00:00Z", "day"},
		{"dst_fall_utc_day", "America/New_York", "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z", "day"},
		{"dst_spring_23h_day", "America/New_York", "2026-03-08T00:00:00-05:00", "2026-03-09T00:00:00-04:00", "day"},
		{"dst_fall_25h_day", "America/New_York", "2026-11-01T00:00:00-04:00", "2026-11-02T00:00:00-05:00", "day"},
		{"dst_repeated_hour", "America/New_York", "2026-11-01T01:00:00-04:00", "2026-11-01T02:00:00-05:00", "hour"},
		{"dst_half_hour_shift", "Australia/Lord_Howe", "2026-10-03T00:00:00Z", "2026-10-05T00:00:00Z", "hour"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tz := useGroupUsageRepositoryTestTimezone(t, tc.zone)
			var queries []string
			matcher := sqlmock.QueryMatcherFunc(func(expected, actual string) error {
				queries = append(queries, actual)
				return sqlmock.QueryMatcherRegexp.Match(expected, actual)
			})
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			start, err := time.Parse(time.RFC3339Nano, tc.start)
			require.NoError(t, err)
			end, err := time.Parse(time.RFC3339Nano, tc.end)
			require.NoError(t, err)
			mock.ExpectQuery(`(?s)FROM usage_logs\s+WHERE created_at >= \$1 AND created_at < \$2`).
				WithArgs(start, end).
				WillReturnRows(sqlmock.NewRows([]string{"date", "requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_tokens", "cost", "actual_cost"}).
					AddRow("2026-09-30", 2, 2, 4, 0, 0, 6, 14, 6))
			repo := &usageLogRepository{sql: db, configuredTimezone: tz}
			trend, err := repo.GetUsageTrendWithUsageFilters(context.Background(), start, end, tc.granularity, usagestats.UsageLogFilters{})
			require.NoError(t, err)
			require.Len(t, queries, 1, "must not probe incompatible local aggregates before falling back")
			require.Contains(t, queries[0], "FROM usage_logs")
			require.Len(t, trend, 1)
			require.EqualValues(t, 2, trend[0].Requests)
			require.InDelta(t, 14, trend[0].Cost, 1e-9)
			require.InDelta(t, 6, trend[0].ActualCost, 1e-9)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
