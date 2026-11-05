package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type performancePauseEvidenceMatcher struct {
	t                      *testing.T
	windowStart, windowEnd time.Time
	evidence               upstreamstats.Evidence
}

func (m performancePauseEvidenceMatcher) Match(v driver.Value) bool {
	raw, ok := v.(string)
	if !ok {
		return false
	}
	var got service.AccountPerformance
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		return false
	}
	require.True(m.t, got.WindowStart.Equal(m.windowStart))
	require.True(m.t, got.WindowEnd.Equal(m.windowEnd))
	require.Equal(m.t, m.evidence.Percent, got.UpstreamError.Percent)
	require.Equal(m.t, m.evidence.Failures, got.UpstreamError.Failures)
	require.Equal(m.t, m.evidence.Total, got.UpstreamError.Total)
	require.Equal(m.t, m.evidence.Source, got.UpstreamError.Source)
	require.True(m.t, m.evidence.ObservedSince.Equal(got.UpstreamError.ObservedSince))
	require.Equal(m.t, m.evidence.WindowComplete, got.UpstreamError.WindowComplete)
	return true
}

func TestAccountPerformanceUpstreamErrorEvaluateUsesDisplayedEvidence(t *testing.T) {
	for _, auditFails := range []bool{false, true} {
		name := "pause_commits"
		if auditFails {
			name = "audit_failure_rolls_back"
		}
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
			now := time.Date(2026, 11, 6, 0, 30, 0, 0, time.UTC)
			since := now.Add(-15 * time.Minute)
			original := upstreamstats.Default
			upstreamstats.Default = upstreamstats.NewStore(20, now.Add(-5*time.Minute))
			t.Cleanup(func() { upstreamstats.Default = original })
			// Older/future samples must not be used. A startup-shortened window
			// still evaluates its 1 failure / 2 observed responses.
			upstreamstats.Default.Record(7, 503, since.Add(-time.Second))
			upstreamstats.Default.Record(7, 503, now.Add(-time.Minute))
			upstreamstats.Default.Record(7, 200, now)
			upstreamstats.Default.Record(7, 503, now.Add(time.Second))
			max := 25.0
			cfg := service.PerformanceConfig{Enabled: true, UpstreamError: service.PerformanceBounds{Max: &max}}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			repo := &accountPerformanceRepository{db: db}
			read := func() {
				mock.ExpectQuery("SELECT value FROM settings").WithArgs(service.SettingKeyAccountPerformanceWindowMinutes).
					WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("15"))
				mock.ExpectQuery("WITH source").WithArgs("{7}", since, now).WillReturnRows(performanceRow(cfg))
			}
			read()
			displayed, err := repo.Read(context.Background(), []int64{7}, now)
			require.NoError(t, err)
			evidence := displayed[7].UpstreamError
			require.Equal(t, int64(2), evidence.Total)
			require.Equal(t, int64(1), evidence.Failures)
			require.NotNil(t, evidence.Percent)
			require.Equal(t, 50.0, *evidence.Percent)
			require.False(t, evidence.WindowComplete)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT id FROM accounts.*FOR SHARE").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
			mock.ExpectQuery("SELECT config,revision,paused,pause_generation.*FOR UPDATE SKIP LOCKED").WithArgs(int64(7)).
				WillReturnRows(sqlmock.NewRows([]string{"config", "revision", "paused", "pause_generation"}).AddRow(string(raw), 1, false, 0))
			read()
			mock.ExpectExec("UPDATE account_performance_monitors SET paused=true").WithArgs(int64(7), now, "upstream_error").WillReturnResult(sqlmock.NewResult(0, 1))
			audit := mock.ExpectExec("INSERT INTO account_performance_events").WithArgs(int64(7), "pause", int64(0), int64(1), int64(1),
				performancePauseEvidenceMatcher{t: t, windowStart: since, windowEnd: now, evidence: evidence})
			if auditFails {
				audit.WillReturnError(errors.New("audit unavailable"))
				mock.ExpectRollback()
				require.Error(t, repo.Evaluate(context.Background(), 7, now))
			} else {
				audit.WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
				require.NoError(t, repo.Evaluate(context.Background(), 7, now))
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAccountPerformanceUpstreamErrorEvaluateMissingAndBoundarySamples(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []int
		min, max float64
	}{
		{"missing_is_unknown", nil, 1, 100},
		{"all_success_equals_zero_min", []int{200, 200}, 0, 0},
		{"equals_error_max", []int{503, 200}, 0, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
			now := time.Date(2026, 11, 6, 0, 30, 0, 0, time.UTC)
			original := upstreamstats.Default
			upstreamstats.Default = upstreamstats.NewStore(20, now.Add(-time.Hour))
			t.Cleanup(func() { upstreamstats.Default = original })
			for _, status := range tc.statuses {
				upstreamstats.Default.Record(7, status, now)
			}
			cfg := service.PerformanceConfig{Enabled: true, UpstreamError: service.PerformanceBounds{Min: &tc.min, Max: &tc.max}}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT id FROM accounts.*FOR SHARE").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
			mock.ExpectQuery("SELECT config,revision,paused,pause_generation.*FOR UPDATE SKIP LOCKED").
				WillReturnRows(sqlmock.NewRows([]string{"config", "revision", "paused", "pause_generation"}).AddRow(string(raw), 1, false, 0))
			mock.ExpectQuery("SELECT value FROM settings").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("15"))
			mock.ExpectQuery("WITH source").WillReturnRows(performanceRow(cfg))
			mock.ExpectRollback()
			require.NoError(t, (&accountPerformanceRepository{db: db}).Evaluate(context.Background(), 7, now))
			require.NoError(t, mock.ExpectationsWereMet(), "no pause, audit or automatic release is allowed")
		})
	}
}
