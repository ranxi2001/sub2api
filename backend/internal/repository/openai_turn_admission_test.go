package repository

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestOpenAITurnAdmissionReadTransaction(t *testing.T) {
	for _, failure := range []string{"", "account", "paused", "groups", "commit"} {
		t.Run(failure, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			defer func() { _ = client.Close() }()
			repo := &accountRepository{client: client}
			mock.ExpectBegin()
			query := mock.ExpectQuery(`SELECT .* FROM "accounts"`)
			if failure == "account" {
				query.WillReturnError(errors.New("read failed"))
				mock.ExpectRollback()
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "type", "status", "schedulable"}).
					AddRow(7, "openai", "oauth", "active", true))
				paused := mock.ExpectQuery(`SELECT account_id FROM account_performance_monitors WHERE paused AND account_id=ANY\(\$1\) UNION SELECT account_id FROM ai_ops_controls WHERE ai_paused AND account_id=ANY\(\$1\)`).WithArgs("{7}")
				if failure == "paused" {
					paused.WillReturnError(errors.New("pause read failed"))
					mock.ExpectRollback()
				} else {
					paused.WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
					groups := mock.ExpectQuery(`SELECT .* FROM "account_groups"`)
					if failure == "groups" {
						groups.WillReturnError(errors.New("group read failed"))
						mock.ExpectRollback()
					} else {
						groups.WillReturnRows(sqlmock.NewRows([]string{"account_id", "group_id"}))
						commit := mock.ExpectCommit()
						if failure == "commit" {
							commit.WillReturnError(errors.New("commit failed"))
						}
					}
				}
			}
			a, parent, err := repo.GetOpenAITurnAdmission(context.Background(), 7)
			if failure == "" {
				require.NoError(t, err)
				require.EqualValues(t, 7, a.ID)
				require.True(t, a.IsSchedulable())
				require.Empty(t, a.GroupIDs)
			} else {
				require.Error(t, err)
				require.Nil(t, a, "no stale/split snapshot fallback")
			}
			require.Nil(t, parent)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpenAITurnAdmissionLoadsOnlyMembershipAndModelRestrictions(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	defer func() { _ = client.Close() }()
	repo := &accountRepository{client: client}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "accounts"`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "type", "status", "schedulable"}).
			AddRow(7, "openai", "oauth", "active", true))
	mock.ExpectQuery(`SELECT account_id FROM account_performance_monitors WHERE paused AND account_id=ANY\(\$1\) UNION SELECT account_id FROM ai_ops_controls WHERE ai_paused AND account_id=ANY\(\$1\)`).WithArgs("{7}").
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	mock.ExpectQuery(`SELECT .* FROM "account_groups"`).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "group_id", "priority", "allowed_models"}).
			AddRow(7, 9, 3, []byte(`["gpt-6-astra"]`)))
	mock.ExpectCommit()

	account, parent, err := repo.GetOpenAITurnAdmission(context.Background(), 7)
	require.NoError(t, err)
	require.Nil(t, parent)
	require.Equal(t, []int64{9}, account.GroupIDs)
	require.Len(t, account.AccountGroups, 1)
	require.Nil(t, account.AccountGroups[0].Group)
	groupID := int64(9)
	require.True(t, account.IsModelAllowedInGroup(&groupID, "gpt-6-astra"))
	require.False(t, account.IsModelAllowedInGroup(&groupID, "gpt-5.5"))
	require.NoError(t, mock.ExpectationsWereMet())
}

// Independent monitor and AI pauses must survive the same repeatable-read
// snapshot used for membership and prevent sending another upstream turn.
func TestOpenAITurnAdmissionPreservesIndependentPause(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	defer func() { _ = client.Close() }()
	repo := &accountRepository{client: client}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "accounts"`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "type", "status", "schedulable"}).
			AddRow(7, "openai", "oauth", "active", true))
	mock.ExpectQuery(`SELECT account_id FROM account_performance_monitors WHERE paused AND account_id=ANY\(\$1\) UNION SELECT account_id FROM ai_ops_controls WHERE ai_paused AND account_id=ANY\(\$1\)`).WithArgs("{7}").
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(7))
	mock.ExpectQuery(`SELECT .* FROM "account_groups"`).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "group_id"}))
	mock.ExpectCommit()

	account, parent, err := repo.GetOpenAITurnAdmission(context.Background(), 7)
	require.NoError(t, err)
	require.Nil(t, parent)
	require.True(t, account.Schedulable, "do not rewrite the administrator's scheduling switch")
	require.True(t, account.PerformancePaused)
	require.False(t, account.IsSchedulable(), "independent pause blocks another upstream turn")
	require.NoError(t, mock.ExpectationsWereMet())
}
