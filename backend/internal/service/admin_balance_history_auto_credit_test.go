//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type autoCreditHistoryRedeemRepo struct {
	RedeemCodeRepository
	codes []RedeemCode
}

func (r *autoCreditHistoryRedeemRepo) ListByUserPaginated(_ context.Context, _ int64, _ pagination.PaginationParams, _ string) ([]RedeemCode, *pagination.PaginationResult, error) {
	return r.codes, &pagination.PaginationResult{Total: int64(len(r.codes))}, nil
}
func (r *autoCreditHistoryRedeemRepo) SumPositiveBalanceByUser(context.Context, int64) (float64, error) {
	return 8000, nil
}

func TestAutoCreditBalanceHistory(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, filter              string
		page, size, offset, limit int
		empty                     bool
	}{
		{"all sources second page", "", 2, 2, 0, 4, false},
		{"credit only", BalanceHistoryTypeAutoCredit, 1, 2, 0, 2, false},
		{"empty page retains total", BalanceHistoryTypeAutoCredit, 3, 2, 4, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			repo := &autoCreditHistoryRedeemRepo{codes: []RedeemCode{{ID: 2, Type: "admin_balance", CreatedAt: now.Add(time.Hour), Value: 8000}}}
			s := &adminServiceImpl{entClient: client, redeemCodeRepo: repo}
			if tc.filter == "" {
				mock.ExpectQuery("SELECT id,.*FROM user_affiliate_ledger").WithArgs(int64(7), 0, 1000).
					WillReturnRows(sqlmock.NewRows([]string{"id", "amount", "created_at"}).AddRow(2, 20, now.Add(-time.Hour)))
				mock.ExpectQuery("SELECT COUNT.*FROM user_affiliate_ledger").WithArgs(int64(7)).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			}
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM auto_credit_ledger WHERE user_id=\$1`).WithArgs(int64(7)).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
			rows := sqlmock.NewRows([]string{"id", "value", "amount", "before", "after", "note", "status", "created_at", "settled_at", "settled_by"})
			if !tc.empty {
				rows.AddRow(2, 1000.00000001, "1000.00000001", "900.00000000", "1900.00000001", "自动充值需索取费用", "pending", now, nil, nil).
					AddRow(1, 1000, "1000.00000000", "999.00000000", "1999.00000000", "自动充值需索取费用", "settled", now.Add(-2*time.Hour), now.Add(2*time.Hour), 9)
			}
			mock.ExpectQuery(`SELECT id,amount::double precision,.*FROM auto_credit_ledger WHERE user_id=\$1 ORDER BY created_at DESC,id DESC OFFSET \$2 LIMIT \$3`).WithArgs(int64(7), tc.offset, tc.limit).WillReturnRows(rows)
			codes, total, recharged, err := s.GetUserBalanceHistory(context.Background(), 7, tc.page, tc.size, tc.filter)
			require.NoError(t, err)
			require.Equal(t, float64(8000), recharged, "credit and collection must not count as another paid recharge")
			if tc.empty {
				require.Empty(t, codes)
				require.EqualValues(t, 2, total)
			} else {
				require.Len(t, codes, 2)
				if tc.filter == "" {
					require.EqualValues(t, 4, total)
					require.Equal(t, RedeemTypeAffiliateBalance, codes[0].Type)
				} else {
					require.EqualValues(t, 2, total)
					require.Equal(t, "1000.00000001", codes[0].AutoCredit.Amount)
					require.Nil(t, codes[0].AutoCredit.SettledAt)
				}
				credit := codes[1]
				require.Equal(t, BalanceHistoryTypeAutoCredit, credit.Type)
				require.Equal(t, "settled", credit.AutoCredit.Status)
				require.EqualValues(t, 1, credit.AutoCredit.LedgerID)
				require.EqualValues(t, 9, *credit.AutoCredit.SettledBy)
				require.Equal(t, now.Add(-2*time.Hour), *credit.UsedAt, "sort by issue time, not settlement time")
				require.Equal(t, "999.00000000", credit.AutoCredit.BalanceBefore)
				require.Equal(t, "1999.00000000", credit.AutoCredit.BalanceAfter)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAutoCreditBalanceHistoryFailsOnLedgerError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	s := &adminServiceImpl{entClient: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
	mock.ExpectQuery("SELECT COUNT.*FROM auto_credit_ledger").WithArgs(int64(7)).WillReturnError(errors.New("ledger unavailable"))
	_, _, _, err = s.GetUserBalanceHistory(context.Background(), 7, 1, 15, BalanceHistoryTypeAutoCredit)
	require.EqualError(t, err, "ledger unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}
