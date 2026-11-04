//go:build unit

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func expectDeletedKeySettlementPrefix(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO usage_billing_dedup").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery("SELECT request_fingerprint.*FROM usage_billing_dedup_archive").
		WillReturnRows(sqlmock.NewRows([]string{"request_fingerprint"}))
	mock.ExpectQuery(conditionalBalanceDeductSQL).
		WithArgs(1.25, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(98.75))
}

// A database failure on a key counter must not turn into a successful,
// partially applied billing transaction.
func TestUsageBillingRepositoryApply_KeyUpdateFailureStillRollsBack(t *testing.T) {
	dbFailure := errors.New("database unavailable")
	for _, field := range []string{"quota", "window"} {
		t.Run(field+"/database_error", func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			cmd := &service.UsageBillingCommand{
				RequestID: "failed-key-settlement", APIKeyID: 7, UserID: 42, BalanceCost: 1.25,
			}
			expectDeletedKeySettlementPrefix(mock)
			if field == "quota" {
				cmd.APIKeyQuotaCost = 1.25
				mock.ExpectQuery("UPDATE api_keys").
					WithArgs(1.25, int64(7), service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted).
					WillReturnError(dbFailure)
			} else {
				cmd.APIKeyRateLimitCost = 1.25
				mock.ExpectExec("UPDATE api_keys").WithArgs(1.25, int64(7)).WillReturnError(dbFailure)
			}
			mock.ExpectRollback()
			result, err := NewUsageBillingRepository(nil, db).Apply(context.Background(), cmd)
			require.Nil(t, result)
			require.ErrorIs(t, err, dbFailure)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// When the key row no longer matches (deleted mid-request), upstream #7816
// skips the key's own counters and still commits the user's charge.
func TestUsageBillingRepositoryApply_MissingKeySkipsKeyCounters(t *testing.T) {
	for _, field := range []string{"quota", "window"} {
		t.Run(field, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			cmd := &service.UsageBillingCommand{
				RequestID: "deleted-key-settlement", APIKeyID: 7, UserID: 42, BalanceCost: 1.25,
			}
			expectDeletedKeySettlementPrefix(mock)
			if field == "quota" {
				cmd.APIKeyQuotaCost = 1.25
				mock.ExpectQuery("UPDATE api_keys").
					WithArgs(1.25, int64(7), service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted).
					WillReturnError(sql.ErrNoRows)
			} else {
				cmd.APIKeyRateLimitCost = 1.25
				mock.ExpectExec("UPDATE api_keys").WithArgs(1.25, int64(7)).
					WillReturnResult(sqlmock.NewResult(0, 0))
			}
			expectNoAutoCreditConfig(mock)
			mock.ExpectCommit()
			result, err := NewUsageBillingRepository(nil, db).Apply(context.Background(), cmd)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.Applied)
			require.NotNil(t, result.NewBalance)
			require.InDelta(t, 98.75, *result.NewBalance, 1e-9)
			require.False(t, result.APIKeyQuotaExhausted)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
