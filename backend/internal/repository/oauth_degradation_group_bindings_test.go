package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func expectOAuthDegradationGroupBindingsAccount(mock sqlmock.Sqlmock, id int64, extra string) {
	mock.ExpectQuery(`SELECT COALESCE\(extra,.*FROM accounts.*FOR NO KEY UPDATE`).WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow(extra))
}

func TestOAuthDegradationGroupRestoreCannotUndoClosedScope(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	tx, err := db.Begin()
	require.NoError(t, err)
	expectOAuthDegradationGroupBindingsAccount(mock, 8, `{"oauth_degradation_action":{"limit_models":false,"limit_concurrency":false,"limit_groups":true,"group_ids":[21]}}`)
	state := qualityState{Removed: []qualityGroup{{AccountID: 8, GroupID: 21}}}
	conflict, err := restoreQualityGroups(context.Background(), tx, 8, &state)
	require.NoError(t, err)
	require.True(t, conflict)
	require.Equal(t, "oauth_degradation_group_limited", state.RestoreDiagnostic.Reason)
	require.Len(t, state.Removed, 1, "the other policy must retain ownership evidence")
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet(), "closed groups cannot be recreated or produce a recovery event")
}
