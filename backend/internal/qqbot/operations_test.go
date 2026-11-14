package qqbot

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestListJobsAcceptsPersonalQueryFilters(t *testing.T) {
	for _, kind := range []string{"account_balance", "today_usage", "lottery_chances"} {
		t.Run(kind, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			c := previewTestConfig()
			expectPreviewConfig(t, mock, c)
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM qq_bot_jobs")).
				WithArgs(c.AppID, "group", "member", "", "", nil, nil, kind, "succeeded").
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			mock.ExpectQuery("SELECT id,app_id,event_type").
				WithArgs(c.AppID, "group", "member", "", "", nil, nil, kind, "succeeded", 10, 0).
				WillReturnRows(sqlmock.NewRows([]string{"id", "app_id", "event_type", "group_id", "member_id", "kind", "status", "error", "input_tokens", "output_tokens", "created_at", "finished_at", "trigger_reason", "decision_reason", "image_decision", "site_query", "chat_execution"}))
			mock.ExpectCommit()
			page, err := New(db, previewEncryptor{}, true, nil).ListJobs(context.Background(), JobFilter{GroupID: "group", MemberID: "member", QueryKind: kind, QueryStatus: "succeeded", Page: 1, PageSize: 10})
			require.NoError(t, err)
			require.Empty(t, page.Items)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestModuleMergeAndIndependentRevision(t *testing.T) {
	a := DefaultConfig()
	a.AppID = "app"
	a.AppSecret = "secret"
	b := a
	b.ChatModel = "changed"
	b.AllowedGroups = []string{"forged"}
	c, err := mergeConfigSection(a, b, "chat")
	require.NoError(t, err)
	require.Equal(t, "changed", c.ChatModel)
	require.Empty(t, c.AllowedGroups)
	require.Equal(t, a.AppSecret, c.AppSecret)
	require.NotEqual(t, sectionVersion(a, "chat"), sectionVersion(c, "chat"))
	require.Equal(t, sectionVersion(a, "connection"), sectionVersion(c, "connection"))
	public := a.Public()
	raw, err := json.Marshal(public)
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"app_secret":"secret"`)
}
func TestLotteryPreviewIncludesGuaranteeReplacement(t *testing.T) {
	c := LotteryConfig{Prizes: []LotteryPrize{{Amount: 1, ProbabilityBPS: 10000}}, GuaranteeEnabled: true, GuaranteeEvery: 10, GuaranteePrizes: []LotteryPrize{{Amount: 5, ProbabilityBPS: 10000}}}
	require.InDelta(t, 1.4, lotteryExpected(c), 1e-8)
	c.GuaranteeEvery = 1
	require.Equal(t, 5.0, lotteryExpected(c))
}
func TestQuietHoursAcrossMidnight(t *testing.T) {
	q := QuietHours{Enabled: true, StartHour: 23, EndHour: 8}
	zone := time.FixedZone("Asia/Shanghai", 28800)
	for _, tc := range []struct {
		hour  int
		quiet bool
	}{{22, false}, {23, true}, {0, true}, {7, true}, {8, false}} {
		require.Equal(t, tc.quiet, q.active(time.Date(2026, 10, 23, tc.hour, 0, 0, 0, zone)))
	}
	q.Enabled = false
	require.False(t, q.active(time.Date(2026, 10, 23, 23, 0, 0, 0, zone)))
}
