package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGatewayManagementRepositoryBindingGuard(t *testing.T) {
	for _, policy := range []string{"explicit-mixed", "oauth-only", "missing", "invalid-platform"} {
		t.Run(policy, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			mock.ExpectQuery(`SELECT COALESCE\(extra,.*FROM accounts.*FOR NO KEY UPDATE`).WithArgs(int64(959)).
				WillReturnRows(sqlmock.NewRows([]string{"extra", "platform", "type"}).AddRow(`{}`, service.PlatformGateway, service.AccountTypeGateway))
			rows := sqlmock.NewRows([]string{"id", "platform", "require_oauth_only"}).AddRow(1, service.PlatformOpenAI, false)
			switch policy {
			case "explicit-mixed":
				rows.AddRow(2, service.PlatformAnthropic, false)
			case "oauth-only":
				rows.AddRow(2, service.PlatformComposite, true)
			case "invalid-platform":
				rows.AddRow(2, service.PlatformGateway, false)
			}
			mock.ExpectQuery(`SELECT id,platform,require_oauth_only FROM groups`).WithArgs("[1,2]").WillReturnRows(rows)
			ids, err := lockOAuthDegradationGroupBindings(context.Background(), db, 959, []int64{1, 2})
			if policy == "explicit-mixed" {
				require.NoError(t, err)
				require.Equal(t, []int64{1, 2}, ids)
			} else {
				require.ErrorIs(t, err, service.ErrGatewayAccountGroup)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
