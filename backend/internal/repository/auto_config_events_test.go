//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAutoConfigHistorySaveIsAtomic(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "history failure rolls back save"}[fail], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			config := service.DefaultOAuthAutoConfig()
			config.Revision = "test-revision"
			raw, err := json.Marshal(config)
			require.NoError(t, err)
			mock.ExpectBegin()
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery("SELECT value FROM settings").WithArgs(service.SettingKeyOAuthAutoConfig).WillReturnRows(sqlmock.NewRows([]string{"value"}))
			mock.ExpectExec("INSERT INTO settings").WithArgs(service.SettingKeyOAuthAutoConfig, string(raw)).WillReturnResult(sqlmock.NewResult(1, 1))
			insert := mock.ExpectExec("INSERT INTO account_auto_config_events").WithArgs(int64(0), "", "openai", service.AutoConfigEventSaved, sqlmock.AnyArg())
			if fail {
				insert.WillReturnError(errors.New("history unavailable"))
				mock.ExpectRollback()
			} else {
				insert.WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			}
			err = NewSettingRepository(client).Set(context.Background(), service.SettingKeyOAuthAutoConfig, string(raw))
			if fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAutoConfigHistoryListCursorAndStructuredDetails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT id,account_id,account_name,platform,kind,details,created_at.*ORDER BY id DESC LIMIT").
		WithArgs(int64(10), service.AutoConfigEventUpgrade, 21).
		WillReturnRows(sqlmock.NewRows([]string{"id", "account_id", "account_name", "platform", "kind", "details", "created_at"}).
			AddRow(9, 7, "example", "openai", service.AutoConfigEventUpgrade, `{"previous_concurrency":3,"concurrency":4,"credentials":{"token":"must-not-be-returned"}}`, now))
	repo := &accountOpsRepository{db: db}
	events, err := repo.ListAutoConfigEvents(context.Background(), 10, service.AutoConfigEventUpgrade, 21)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, 3, events[0].Details.PreviousConcurrency)
	require.Equal(t, 4, events[0].Details.Concurrency)
	raw, err := json.Marshal(events)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "must-not-be-returned")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAutoConfigHistoryListEmptyIsArray(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("SELECT id,account_id").WithArgs(int64(0), "", 20).WillReturnRows(sqlmock.NewRows([]string{"id", "account_id", "account_name", "platform", "kind", "details", "created_at"}))
	events, err := (&accountOpsRepository{db: db}).ListAutoConfigEvents(context.Background(), 0, "", 20)
	require.NoError(t, err)
	require.NotNil(t, events)
	require.Empty(t, events)
	require.NoError(t, mock.ExpectationsWereMet())
}
