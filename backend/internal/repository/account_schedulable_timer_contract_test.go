package repository

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/accounttimer"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func timerContractRepository(t *testing.T, matcher sqlmock.QueryMatcher) (*accountRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, m, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { require.NoError(t, m.ExpectationsWereMet()); _ = client.Close() })
	return newAccountRepositoryWithSQL(client, db, nil), m
}
func timerContractAccountRows(status string, schedulable bool) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows(dbaccount.Columns).AddRow(
		int64(7), now, now, nil, "after", nil, service.PlatformAnthropic, service.AccountTypeAPIKey,
		[]byte(`{}`), []byte(`{}`), nil, nil, 1, nil, 1, 1.0, 1.0,
		status, nil, nil, nil, false, schedulable, nil, nil, nil, nil, nil, nil, nil, nil, nil, service.QuotaDimensionGlobal,
	)
}
func TestAccountTimerRepositoryOrdinaryEditDoesNotWriteSchedulingBit(t *testing.T) {
	for _, status := range []string{service.StatusActive, service.StatusError} {
		t.Run(status, func(t *testing.T) {
			matcher := sqlmock.QueryMatcherFunc(func(expected, actual string) error {
				if expected == "ordinary edit SQL" {
					if !strings.HasPrefix(actual, `UPDATE "accounts" SET `) {
						return fmt.Errorf("unexpected edit query: %s", actual)
					}
					setsScheduling := strings.Contains(actual, `"schedulable" =`)
					if setsScheduling != (status == service.StatusError) {
						return fmt.Errorf("wrong schedulable write for %s: %s", status, actual)
					}
					return nil
				}
				return sqlmock.QueryMatcherRegexp.Match(expected, actual)
			})
			repo, m := timerContractRepository(t, matcher)
			m.ExpectBegin()
			m.ExpectQuery(`(?s)SELECT.*FOR NO KEY UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"identity", "ollama_group", "ollama_proxy", "enabled", "rate_sync", "snapshot", "session", "auto", "ollama_snapshot", "opencode_group", "opencode_auto", "opencode_snapshot", "current_extra", "concurrency"}).AddRow(true, false, true, nil, nil, nil, nil, nil, nil, false, nil, nil, []byte(`{}`), 3))
			m.ExpectExec("ordinary edit SQL").WillReturnResult(sqlmock.NewResult(0, 1))
			storedScheduling := status != service.StatusError
			m.ExpectQuery(`SELECT .* FROM "accounts" WHERE "id" = \$1`).WithArgs(int64(7)).WillReturnRows(timerContractAccountRows(status, storedScheduling))
			m.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnResult(sqlmock.NewResult(1, 1))
			m.ExpectCommit()
			stale := &service.Account{ID: 7, Name: "after", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: status, Schedulable: false, Credentials: map[string]any{}, Extra: map[string]any{}, Concurrency: 1, Priority: 1}
			ctx := accounttimer.WithPreservedSchedulable(context.Background())
			require.NoError(t, repo.UpdateWithAccountBillingSettings(ctx, stale, nil, nil, nil))
			require.Equal(t, storedScheduling, stale.Schedulable, "response must reflect the locked database result")
		})
	}
}
func TestAccountTimerRepositoryImmediateSwitchCancelsWithinOutboxTransaction(t *testing.T) {
	for _, outboxFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("outbox_failure_%t", outboxFails), func(t *testing.T) {
			repo, m := timerContractRepository(t, sqlmock.QueryMatcherRegexp)
			m.ExpectBegin()
			m.ExpectQuery("SELECT status, expires_at, deleted_at FROM accounts").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "deleted_at"}).AddRow(service.StatusActive, nil, nil))
			m.ExpectExec("WITH cancelled AS").WithArgs(int64(7), int64(19), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 2))
			m.ExpectExec("UPDATE accounts SET schedulable").WithArgs(false, sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
			event := m.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox"))
			if outboxFails {
				event.WillReturnError(errors.New("fixture outbox unavailable"))
				m.ExpectRollback()
			} else {
				event.WillReturnResult(sqlmock.NewResult(1, 1))
				m.ExpectCommit()
			}
			err := repo.SetSchedulable(accounttimer.WithManualIntent(context.Background(), 19), 7, false)
			if outboxFails {
				require.ErrorContains(t, err, "fixture outbox unavailable")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
func TestAccountTimerRepositoryBulkSwitchCancelsOnlyWithManualIntent(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual_%t", manual), func(t *testing.T) {
			repo, m := timerContractRepository(t, sqlmock.QueryMatcherRegexp)
			m.ExpectBegin()
			if manual {
				m.ExpectQuery("SELECT id FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
				m.ExpectExec("WITH cancelled AS").WithArgs(int64(7), int64(19), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 2))
			}
			m.ExpectExec("UPDATE accounts SET").WillReturnResult(sqlmock.NewResult(0, 1))
			m.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnResult(sqlmock.NewResult(1, 1))
			m.ExpectCommit()
			ctx := context.Background()
			if manual {
				ctx = accounttimer.WithManualIntent(ctx, 19)
			}
			disabled := false
			n, err := repo.BulkUpdate(ctx, []int64{7}, service.AccountBulkUpdate{Schedulable: &disabled})
			require.NoError(t, err)
			require.Equal(t, int64(1), n)
		})
	}
}
