//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func aiOpsIntegrationAccount(t *testing.T) int64 {
	t.Helper()
	var id int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,status,schedulable,concurrency,load_factor) VALUES($1,'openai','oauth','active',true,20,10) RETURNING id`, fmt.Sprintf("ai-ops-%d", time.Now().UnixNano())).Scan(&id))
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`, id)
		_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, id)
	})
	return id
}

func TestAIOpsPostgresRevisionSeparatesConfigurationFromObservation(t *testing.T) {
	id := aiOpsIntegrationAccount(t)
	ctx := context.Background()
	read := func() int64 {
		var n int64
		require.NoError(t, integrationDB.QueryRow(`SELECT ai_ops_revision FROM accounts WHERE id=$1`, id).Scan(&n))
		return n
	}
	before := read()
	_, err := integrationDB.Exec(`UPDATE accounts SET last_used_at=NOW(),extra=extra||jsonb_build_object('codex_primary_used_percent',31,'codex_usage_updated_at',NOW(),'codex_turn_ticket:fixture','redacted') WHERE id=$1`, id)
	require.NoError(t, err)
	require.Equal(t, before, read(), "quota/ticket/last-used observations must not invalidate ownership")
	_, err = integrationDB.Exec(`UPDATE accounts SET concurrency=19 WHERE id=$1`, id)
	require.NoError(t, err)
	require.Equal(t, before+1, read())
	var group int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO groups(name,platform) VALUES($1,'openai') RETURNING id`, fmt.Sprintf("ai-scope-%d", id)).Scan(&group))
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM groups WHERE id=$1`, group) })
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, id, group)
	require.NoError(t, err)
	require.Equal(t, before+2, read())
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_groups SET priority=priority+1 WHERE account_id=$1 AND group_id=$2`, id, group)
	require.NoError(t, err)
	require.Equal(t, before+3, read())
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=$2`, id, group)
	require.NoError(t, err)
	require.Equal(t, before+4, read())
}

func TestAIOpsPostgresRuntimePauseDoesNotOverwriteManualSchedulable(t *testing.T) {
	id := aiOpsIntegrationAccount(t)
	repo := &accountRepository{sql: integrationDB}
	_, err := integrationDB.Exec(`INSERT INTO ai_ops_controls(account_id,ai_paused) VALUES($1,true)`, id)
	require.NoError(t, err)
	got, err := repo.AIOpsRuntime(context.Background(), []int64{id})
	require.NoError(t, err)
	require.True(t, got[id].Paused)
	require.Equal(t, 20, got[id].Concurrency)
	var schedulable bool
	require.NoError(t, integrationDB.QueryRow(`SELECT schedulable FROM accounts WHERE id=$1`, id).Scan(&schedulable))
	require.True(t, schedulable)
	_, err = integrationDB.Exec(`UPDATE accounts SET schedulable=false WHERE id=$1`, id)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE ai_ops_controls SET ai_paused=false WHERE account_id=$1`, id)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRow(`SELECT schedulable FROM accounts WHERE id=$1`, id).Scan(&schedulable))
	require.False(t, schedulable, "AI resume cannot clear a manual restriction")
}

func TestAIOpsPostgresNoEvidenceNeverBecomesZeroErrorHealthy(t *testing.T) {
	id := aiOpsIntegrationAccount(t)
	repo := &accountRepository{sql: integrationDB}
	tx, err := repo.aiOpsTx(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	p := aiOpsTestPolicy()
	p.Accounts = []service.AIOpsAccountScope{{AccountID: id, CapacityDomain: "fixture"}}
	accounts, err := readAIOpsAccounts(context.Background(), tx, []int64{id})
	require.NoError(t, err)
	e, err := captureAIOpsEvidence(context.Background(), tx, id, p, time.Now().UTC(), accounts)
	require.NoError(t, err)
	require.Equal(t, 0, e.Samples)
	require.False(t, e.ModelCoverageComplete)
	require.Equal(t, -1, e.Inflight)
}
