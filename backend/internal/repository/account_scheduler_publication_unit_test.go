//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestSchedulerPublicationLockRoundTripAndRollback(t *testing.T) {
	for _, failLock := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish", true: "lock_failure"}[failLock], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			repo := newAccountRepositoryWithSQL(client, db, nil)
			mock.ExpectBegin()
			// Duplicates and invalid IDs must not add lock calls. The existing
			// namespace and global order must match single-account publishers.
			lock := mock.ExpectExec("SELECT pg_advisory_xact_lock\\(lock_key\\)").WithArgs(pq.Array([]int64{
				int64(0x5780000000000000) ^ 2, int64(0x5780000000000000) ^ 9,
			}))
			if failLock {
				lock.WillReturnError(errors.New("synthetic lock failure"))
				mock.ExpectRollback()
			} else {
				lock.WillReturnResult(sqlmock.NewResult(0, 2))
				mock.ExpectQuery("SELECT .* FROM .*accounts").WillReturnRows(sqlmock.NewRows([]string{"id"}))
				mock.ExpectCommit()
			}
			called := false
			err = repo.WithLatestSchedulerAccounts(context.Background(), []int64{9, 2, 9, 0, -1}, func(accounts []service.Account) error {
				called = true
				require.Empty(t, accounts)
				return nil
			})
			if failLock {
				require.EqualError(t, err, "synthetic lock failure")
				require.False(t, called, "failed lock acquisition must not publish")
			} else {
				require.NoError(t, err)
				require.True(t, called)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
