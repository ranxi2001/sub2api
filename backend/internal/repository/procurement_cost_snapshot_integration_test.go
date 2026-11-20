//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func procurementFixtureSnapshot(platform string, factor *float64) *service.ProcurementCostSnapshot {
	return &service.ProcurementCostSnapshot{Version: 1, AccountType: "gateway", Provider: "new_api", TargetPlatform: platform, Basis: "total_cost", CostMultiplier: factor}
}

func TestProcurementSnapshotAllInsertPathsAndImmutability(t *testing.T) {
	f := newChannelOperationsFixture(t)
	repo := newUsageLogRepositoryWithSQL(integrationEntClient, integrationDB)
	for _, mode := range []string{"single", "batch", "best-effort-single", "best-effort-batch", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			factor := .7
			log := &service.UsageLog{UserID: f.user, APIKeyID: f.key, AccountID: f.account, RequestID: fmt.Sprintf("procurement-%d-%s", f.account, mode),
				Platform: "openai", Model: "fixture-model", TotalCost: 10, ActualCost: 3, CreatedAt: time.Now().UTC()}
			if mode != "legacy" {
				log.ProcurementCostSnapshot = procurementFixtureSnapshot("openai", &factor)
			}
			prepared := prepareUsageLogInsert(log)
			var err error
			switch mode {
			case "single", "legacy":
				_, err = repo.createSingle(t.Context(), integrationDB, log)
			case "batch":
				key := usageLogBatchKey(log.RequestID, log.APIKeyID)
				_, _, _, err = repo.batchInsertUsageLogs(integrationDB, []string{key}, map[string]usageLogInsertPrepared{key: prepared})
			case "best-effort-single":
				err = execUsageLogInsertNoResult(t.Context(), integrationDB, prepared)
			case "best-effort-batch":
				query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
				_, err = integrationDB.ExecContext(t.Context(), query, args...)
			}
			require.NoError(t, err)
			stored, err := scanUsageLog(integrationDB.QueryRowContext(t.Context(), "SELECT "+usageLogSelectColumns+" FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", log.RequestID, f.key))
			require.NoError(t, err)
			require.Equal(t, log.ProcurementCostSnapshot, stored.ProcurementCostSnapshot)
			require.Equal(t, 10.0, stored.TotalCost)
			require.Equal(t, 3.0, stored.ActualCost)
			if mode != "legacy" {
				log.ProcurementCostSnapshot = procurementFixtureSnapshot("openai", nil)
				_, err = repo.createSingle(t.Context(), integrationDB, log)
				require.NoError(t, err)
				var saved float64
				require.NoError(t, integrationDB.QueryRow(`SELECT (procurement_cost_snapshot->>'cost_multiplier')::numeric FROM usage_logs WHERE id=$1`, stored.ID).Scan(&saved))
				require.Equal(t, .7, saved, "dedup must retain the first snapshot")
				_, err = integrationDB.Exec(`UPDATE usage_logs SET procurement_cost_snapshot=NULL WHERE id=$1`, stored.ID)
				require.ErrorContains(t, err, "immutable")
			}
		})
	}
}

func TestProcurementSnapshotDurableReplayDoesNotRepriceOrRecharge(t *testing.T) {
	repo, cmd, usage, _ := durableBillingFixture(t)
	factor := .7
	usage.Platform, usage.TotalCost, usage.ActualCost = "openai", 10, 3
	cmd.BalanceCost, cmd.APIKeyQuotaCost = 3, 3
	usage.ProcurementCostSnapshot = procurementFixtureSnapshot("openai", &factor)
	require.NoError(t, repo.StageUsageBilling(t.Context(), &cmd, &usage))
	usage.ProcurementCostSnapshot = procurementFixtureSnapshot("kimi", nil)
	require.NoError(t, repo.StageUsageBilling(t.Context(), &cmd, &usage))
	key := service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID}
	for i := 0; i < 2; i++ {
		_, err := repo.ApplyStagedUsageBilling(t.Context(), key)
		require.NoError(t, err)
	}
	stored, err := scanUsageLog(repo.db.QueryRowContext(t.Context(), "SELECT "+usageLogSelectColumns+" FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", cmd.RequestID, cmd.APIKeyID))
	require.NoError(t, err)
	require.Equal(t, "openai", stored.ProcurementCostSnapshot.TargetPlatform)
	require.Equal(t, .7, *stored.ProcurementCostSnapshot.CostMultiplier)
	var balance, quota float64
	require.NoError(t, repo.db.QueryRow(`SELECT balance FROM users WHERE id=$1`, cmd.UserID).Scan(&balance))
	require.NoError(t, repo.db.QueryRow(`SELECT quota_used FROM api_keys WHERE id=$1`, cmd.APIKeyID).Scan(&quota))
	require.Equal(t, 97.0, balance, "user charge is actual_cost=3, never procurement 7")
	require.Equal(t, 3.0, quota, "replay cannot double-charge")
}

func TestProcurementSnapshotProfitUsesMixedRequestTargetsAndUnknowns(t *testing.T) {
	f := newChannelOperationsFixture(t)
	now := time.Now().UTC()
	_, err := integrationDB.Exec(`UPDATE accounts SET type='gateway',platform='gateway',credentials='{"upstream_kind":"new_api","base_url":"https://gateway.example/tenant","api_key":"synthetic-only"}',extra='{"cost_multiplier":99}',rate_multiplier=77 WHERE id=$1`, f.account)
	require.NoError(t, err)
	repo := newUsageLogRepositoryWithSQL(integrationEntClient, integrationDB)
	for i, target := range []string{"openai", "kimi"} {
		factor := []float64{.7, .1}[i]
		_, err = repo.createSingle(t.Context(), integrationDB, &service.UsageLog{UserID: f.user, APIKeyID: f.key, AccountID: f.account,
			RequestID: fmt.Sprintf("procurement-mixed-%d-%d", f.account, i), Platform: target, Model: "fixture-model", TotalCost: 10, ActualCost: 3,
			CreatedAt: now.Add(-time.Hour), ProcurementCostSnapshot: procurementFixtureSnapshot(target, &factor)})
		require.NoError(t, err)
	}
	filter := service.UpstreamOperationsFilter{From: now.Add(-24 * time.Hour), Until: now, AccountID: f.account, Dimension: "account", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 20}
	read := func() map[string]any {
		out, err := f.repo.ReadProfitOperations(t.Context(), filter)
		require.NoError(t, err)
		var data map[string]any
		require.NoError(t, json.Unmarshal(out.Usage, &data))
		return data["summary"].(map[string]any)
	}
	summary := read()
	require.Equal(t, 8.0, summary["estimated_cost"], "10*.7 + 10*.1 exactly once; never account debit 77 or current cost 99")
	require.Equal(t, 6.0, summary["request_income"])
	require.Equal(t, -2.0, summary["estimated_profit"])
	_, err = integrationDB.Exec(`UPDATE accounts SET extra='{"cost_multiplier":0.01}',rate_multiplier=1 WHERE id=$1`, f.account)
	require.NoError(t, err)
	require.Equal(t, summary["estimated_cost"], read()["estimated_cost"], "account edits cannot reprice captured requests")
	factor := .7
	future := procurementFixtureSnapshot("openai", &factor)
	future.Version = 99
	for i, snapshot := range []*service.ProcurementCostSnapshot{nil, procurementFixtureSnapshot("openai", nil), future, procurementFixtureSnapshot("kimi", &factor)} {
		_, err = repo.createSingle(t.Context(), integrationDB, &service.UsageLog{UserID: f.user, APIKeyID: f.key, AccountID: f.account,
			RequestID: fmt.Sprintf("procurement-unknown-%d-%d", f.account, i), Platform: "openai", Model: "fixture-model", TotalCost: 10, ActualCost: 3,
			CreatedAt: now.Add(-time.Hour), ProcurementCostSnapshot: snapshot})
		require.NoError(t, err)
	}
	summary = read()
	require.Nil(t, summary["estimated_cost"])
	require.Equal(t, 4.0, summary["unknown_cost_requests"], "missing, captured unknown, unsupported version and mismatched target all stay unknown")
	require.Equal(t, 8.0, summary["covered_estimated_cost"])
}

func TestProcurementSnapshotMigrationLeavesHistoryAndChargesUntouched(t *testing.T) {
	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`CREATE TEMP TABLE usage_logs(id bigint,total_cost numeric,actual_cost numeric) ON COMMIT DROP; INSERT INTO usage_logs VALUES(1,10,3)`)
	require.NoError(t, err)
	sql, err := migrations.FS.ReadFile("392_usage_procurement_cost_snapshot.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.Exec(string(sql))
		require.NoError(t, err)
	}
	var unchanged bool
	require.NoError(t, tx.QueryRow(`SELECT procurement_cost_snapshot IS NULL AND total_cost=10 AND actual_cost=3 FROM usage_logs WHERE id=1`).Scan(&unchanged))
	require.True(t, unchanged)
}
