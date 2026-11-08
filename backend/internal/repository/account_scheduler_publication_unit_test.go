//go:build unit

package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// A failed publication must identify the stalled stage without exposing the
// account IDs or a driver/callback error that may contain sensitive row data.
func TestSchedulerPublicationDiagnostics(t *testing.T) {
	for _, failure := range []string{"begin", "lock", "read", "publish", "complete"} {
		t.Run(failure, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			repo := newAccountRepositoryWithSQL(client, db, nil)
			privateError := errors.New("synthetic-private-token-and-driver-detail")
			begin := mock.ExpectBegin()
			if failure == "begin" {
				begin.WillReturnError(privateError)
			} else {
				lock := mock.ExpectExec("SELECT pg_advisory_xact_lock\\(lock_key\\)")
				if failure == "lock" {
					lock.WillReturnError(context.DeadlineExceeded)
				} else {
					lock.WillReturnResult(sqlmock.NewResult(0, 2))
					read := mock.ExpectQuery("SELECT .* FROM .*accounts")
					if failure == "read" {
						read.WillReturnError(privateError)
					} else {
						read.WillReturnRows(sqlmock.NewRows([]string{"id"}))
					}
				}
				if failure == "complete" {
					mock.ExpectCommit()
				} else {
					mock.ExpectRollback()
				}
			}
			called := false
			err = repo.WithLatestSchedulerAccounts(context.Background(), []int64{987654321012345, 876543210123456, 987654321012345}, func([]service.Account) error {
				called = true
				if failure == "publish" {
					return privateError
				}
				return nil
			})
			if failure == "complete" {
				require.NoError(t, err)
			} else if failure == "lock" {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			} else {
				require.ErrorIs(t, err, privateError)
			}
			require.Equal(t, failure == "publish" || failure == "complete", called)
			require.NoError(t, mock.ExpectationsWereMet())

			var record map[string]any
			require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record))
			require.Equal(t, "[Scheduler] account publication timing", record["msg"])
			require.Equal(t, failure, record["phase"])
			require.Equal(t, float64(3), record["requested_count"])
			require.Equal(t, failure != "begin", record["owns_transaction"])
			require.Equal(t, failure != "begin", record["lock_sql_attempted"])
			require.Equal(t, failure != "begin" && failure != "lock", record["lock_acquired"])
			require.Equal(t, failure == "publish" || failure == "complete", record["publish_attempted"])
			if failure != "begin" {
				require.Equal(t, float64(2), record["lock_count"], "counts must describe deduplicated lock keys")
			}
			if failure == "lock" {
				require.Equal(t, "deadline", record["failure_kind"])
			} else if failure == "complete" {
				require.Equal(t, "none", record["failure_kind"])
			} else {
				require.Equal(t, "error", record["failure_kind"])
			}
			for _, secret := range []string{"987654321012345", "876543210123456", privateError.Error()} {
				require.NotContains(t, output.String(), secret)
			}
		})
	}
}

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
