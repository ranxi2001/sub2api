package repository

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMoveExcelBPSOn403RollsBackFailures(t *testing.T) {
	for _, failureAt := range []string{"delete", "insert", "account", "outbox", "commit"} {
		t.Run(failureAt, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			failure := errors.New("database unavailable")
			mock.ExpectBegin()
			mock.ExpectQuery("(?s)SELECT id FROM groups.*FOR SHARE").WithArgs(int64(7)).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
			mock.ExpectQuery("(?s)SELECT id FROM accounts.*FOR UPDATE").WithArgs(int64(27), "{\"access_token\":\"test-token\"}", "7").
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(27))
			mock.ExpectQuery("SELECT group_id FROM account_groups.*FOR UPDATE").WithArgs(int64(27)).
				WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(3))
			for _, stage := range []struct{ name, query string }{
				{"delete", "DELETE FROM account_groups"},
				{"insert", "INSERT INTO account_groups"},
				{"account", "UPDATE accounts SET extra = COALESCE(extra, '{}'::jsonb) || jsonb_build_object('openai_excel_bps_403_moved_at', $2::text, 'openai_excel_bps_403_moved_group_id', $3::bigint), updated_at = NOW() WHERE id = $1"},
				{"outbox", "INSERT INTO scheduler_outbox"},
			} {
				expect := mock.ExpectExec(regexp.QuoteMeta(stage.query))
				if stage.name == "account" {
					expect = expect.WithArgs(int64(27), recentUTCTimestampArg{}, int64(7))
				}
				if stage.name == failureAt {
					expect.WillReturnError(failure)
					break
				}
				expect.WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if failureAt == "commit" {
				mock.ExpectCommit().WillReturnError(failure)
			} else {
				mock.ExpectRollback()
			}
			account := &service.Account{ID: 27, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "test-token"}, GroupIDs: []int64{3},
				Extra: map[string]any{"openai_excel_bps": true, service.ExcelBPSAutoMoveOn403Key: true, service.ExcelBPS403TargetGroupIDKey: 7}}
			repo := newAccountRepositoryWithSQL(client, db, nil)
			changed, err := repo.MoveExcelBPSOn403(context.Background(), account)
			require.ErrorIs(t, err, failure)
			require.False(t, changed)
			require.Equal(t, []int64{3}, account.GroupIDs)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSchedulerMetadataAccountKeepsExcelBPS403GroupAction(t *testing.T) {
	for _, target := range []int64{0, 7} {
		account := service.Account{ID: 27, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, GroupIDs: []int64{3},
			Extra: map[string]any{"openai_excel_bps": true, service.ExcelBPSAutoMoveOn403Key: true, service.ExcelBPS403TargetGroupIDKey: target, "unused": "drop"}}
		metadata := buildSchedulerMetadataAccount(account)
		data, err := json.Marshal(metadata)
		require.NoError(t, err)
		var restored service.Account
		require.NoError(t, json.Unmarshal(data, &restored))
		got, enabled := restored.ExcelBPS403GroupTarget()
		require.True(t, enabled)
		require.Equal(t, target, got)
		require.Equal(t, account.GroupIDs, restored.GroupIDs)
		require.NotContains(t, restored.Extra, "unused")
	}
}
