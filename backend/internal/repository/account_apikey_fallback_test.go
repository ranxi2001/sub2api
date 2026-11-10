package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyFallbackChangedRowsControlSchedulingPublication(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed int64
		err     error
	}{
		{name: "stale configuration or ineligible account"},
		{name: "eligible selected account", changed: 1},
		{name: "write failure", err: errors.New("write failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			expect := mock.ExpectExec("UPDATE accounts SET schedulable=TRUE,updated_at=NOW\\(\\)").WithArgs(int64(10), service.SettingKeyOAuthAutoConfig, "revision")
			if tc.err != nil {
				expect.WillReturnError(tc.err)
			} else {
				expect.WillReturnResult(sqlmock.NewResult(0, tc.changed))
			}
			if tc.changed > 0 {
				mock.ExpectExec("INSERT INTO scheduler_outbox").WillReturnResult(sqlmock.NewResult(1, 1))
			}
			repo := &accountRepository{sql: db}
			changed, err := repo.EnableOpenAIAPIKeyFallback(context.Background(), 10, "revision")
			require.Equal(t, tc.changed > 0, changed)
			if tc.err != nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
