//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDomesticRouteClaimAndEditFence(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// Use the real schema and transaction-local changes to isolate the claims.
	repo := &accountRepository{sql: tx}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO accounts(name,type,platform,credentials,extra,concurrency,priority,status,schedulable)
	VALUES ('route fixture','apikey','deepseek','{}','{"domestic_route_auto_schedule":true}',1,1,'active',false) RETURNING id`).Scan(&id)
	require.NoError(t, err)
	claims, err := repo.ClaimDomesticRouteProbes(ctx)
	require.NoError(t, err)
	var claim service.DomesticRouteClaim
	for _, c := range claims {
		if c.AccountID == id {
			claim = c
		}
	}
	require.Equal(t, id, claim.AccountID)
	again, err := repo.ClaimDomesticRouteProbes(ctx)
	require.NoError(t, err)
	for _, c := range again {
		require.NotEqual(t, id, c.AccountID)
	}
	require.NoError(t, repo.ApplyDomesticRouteProbe(ctx, claim, true))
	var on bool
	read := func() {
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT schedulable FROM accounts WHERE id=$1`, id).Scan(&on))
	}
	read()
	require.True(t, on)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra='{}',schedulable=false WHERE id=$1`, id)
	require.NoError(t, err)
	require.NoError(t, repo.ApplyDomesticRouteProbe(ctx, claim, true))
	read()
	require.False(t, on)
}
