package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPelicanClaimUsesDatabaseLeaseAndSavedVersion(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := NewScheduledTestPlanRepository(db)
	now := time.Now()
	until := now.Add(15 * time.Minute)
	next := now.Add(30 * time.Minute)
	plan := &service.ScheduledTestPlan{ID: 1, UpdatedAt: now.Add(-time.Hour)}
	query := `(?s)UPDATE scheduled_test_plans.*enabled = true.*running_until IS NULL.*updated_at = \$5.*deleted_at IS NULL`
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WithArgs(plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery(query).WithArgs(plan.ID, now, until, next, plan.UpdatedAt, nil, false, nil, false, "").WillReturnRows(sqlmock.NewRows([]string{"manual_quality_run", "manual_quality_run_id"}).AddRow(false, ""))
	mock.ExpectCommit()
	ok, err := repo.ClaimPelican(context.Background(), plan, now, until, next)
	require.NoError(t, err)
	require.True(t, ok)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WithArgs(plan.AccountID).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery(query).WithArgs(plan.ID, now, until, next, plan.UpdatedAt, nil, false, nil, false, "").WillReturnRows(sqlmock.NewRows([]string{"manual_quality_run", "manual_quality_run_id"}))
	mock.ExpectCommit()
	ok, err = repo.ClaimPelican(context.Background(), plan, now, until, next)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestPelicanCleanupIncludesPausedPlansAndBoundsBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	mock.ExpectExec(`(?s)DELETE FROM scheduled_test_results.*WHERE plans.pelican_config IS NOT NULL AND results.created_at < \$1\s+ORDER BY results.created_at LIMIT 1000`).WithArgs(cutoff).WillReturnResult(sqlmock.NewResult(0, 3))
	require.NoError(t, NewScheduledTestResultRepository(db).PruneExpiredPelican(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestPelicanHistoryPassesAccountFilterWithCursor(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	now := time.Now()
	columns := []string{"id", "plan_id", "status", "error_message", "latency_ms", "started_at", "finished_at", "created_at", "pelican_config", "account_id", "account_name"}
	query := `(?s)FROM scheduled_test_results r.*AND \(\$1::bigint = 0 OR r.id < \$1\)\s+AND \(\$3::bigint = 0 OR p.account_id = \$3\)\s+ORDER BY r.id DESC LIMIT \$2`
	mock.ExpectQuery(query).WithArgs(int64(90), 101, int64(42)).WillReturnRows(sqlmock.NewRows(columns).
		AddRow(int64(89), int64(7), "success", "", int64(1200), now, now, now, []byte(`{"model_id":"gpt-6-astra"}`), int64(42), "Account 42"))
	// Zero keeps the all-accounts dashboard query unchanged.
	mock.ExpectQuery(query).WithArgs(int64(0), 101, int64(0)).WillReturnRows(sqlmock.NewRows(columns))
	repo := NewScheduledTestResultRepository(db)
	items, err := repo.ListPelicanHistory(context.Background(), 90, 101, 42)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int64(42), items[0].AccountID)
	require.Equal(t, "gpt-6-astra", items[0].PelicanConfig.ModelID)
	items, err = repo.ListPelicanHistory(context.Background(), 0, 101, 0)
	require.NoError(t, err)
	require.Empty(t, items)
	require.NoError(t, mock.ExpectationsWereMet())
}
