//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProfitOperationsAccountUsageRollup(t *testing.T) {
	f := newChannelOperationsFixture(t)
	ctx := context.Background()
	from := time.Now().UTC().Truncate(5 * time.Minute).Add(-20 * time.Minute)
	filter := service.UpstreamOperationsFilter{From: from, Until: from.Add(5 * time.Minute), AccountID: f.account, AccountUsage: true,
		Dimension: "account", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 20}
	_, err := integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,account_stats_cost,account_rate_multiplier,created_at)
 VALUES($1,$2,$3,'financial-rollup','fixture',50,10,4,0.5,$4)`, f.user, f.key, f.account, from.Add(time.Minute))
	require.NoError(t, err)
	_, err = f.repo.ReadProfitOperations(ctx, filter)
	require.ErrorIs(t, err, service.ErrAccountUsageBuilding)
	worker := newDashboardAggregationRepositoryWithSQL(integrationDB)
	check := func(requests int, income, cost, profit float64) {
		t.Helper()
		for i := 0; i < 10; i++ {
			var pending bool
			require.NoError(t, integrationDB.QueryRow("SELECT EXISTS(SELECT 1 FROM account_usage_financial_pending WHERE bucket=$1)", from).Scan(&pending))
			if !pending {
				break
			}
			_, err := worker.RefreshAccountUsageFinancial(ctx)
			require.NoError(t, err)
		}
		report, err := f.repo.ReadProfitOperations(ctx, filter)
		require.NoError(t, err)
		var result struct {
			Summary struct {
				Requests int
				Income   float64 `json:"request_income"`
				Cost     float64 `json:"upstream_cost"`
				Profit   float64 `json:"gross_profit"`
			}
		}
		require.NoError(t, json.Unmarshal(report.Usage, &result))
		require.Equal(t, requests, result.Summary.Requests)
		require.Equal(t, income, result.Summary.Income)
		require.Equal(t, cost, result.Summary.Cost)
		require.Equal(t, profit, result.Summary.Profit)
	}
	check(1, 10, 2, 8)
	_, err = integrationDB.Exec(`UPDATE accounts SET type='oauth',extra='{"cost_multiplier":0.25}',rate_multiplier=3 WHERE id=$1`, f.account)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,account_stats_cost,account_rate_multiplier,created_at)
 VALUES($1,$2,$3,'one-times','fixture',50,12,8,1,$4)`, f.user, f.key, f.account, from.Add(time.Minute))
	require.NoError(t, err)
	check(2, 22, 4, 18) // 4 × 0.5 + 8 × 1 × 0.25
	_, err = integrationDB.Exec("UPDATE account_usage_financial_rollups SET procurement_source=NULL WHERE bucket=$1 AND account_id=$2", from, f.account)
	require.NoError(t, err)
	check(2, 22, 4, 18)
	filter.From = from.Add(time.Minute)
	check(2, 22, 4, 18)
	filter.From = from
	// The two older rows cost 2 (non-unit account debit) and 50 × .25.
	// Capture both bases in new snapshots; keep the old snapshot's own rate.
	_, err = integrationDB.Exec(`UPDATE accounts SET extra='{"cost_multiplier":0.25,"token_procurement_basis":"standard_charge"}' WHERE id=$1`, f.account)
	require.NoError(t, err)
	check(2, 22, 14.5, 7.5)
	_, err = integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,account_stats_cost,account_rate_multiplier,billing_mode,procurement_cost_snapshot,created_at)
 SELECT $1,$2,$3,'token-basis-'||v.label,'fixture',100,50,15,1,v.mode,v.snap::jsonb,$4
 FROM (VALUES
 ('old','token','{"version":2,"basis":"account_usage","cost_multiplier":0.35}'),
 ('fixed','token','{"version":2,"basis":"account_usage","token_basis":"standard_charge","cost_multiplier":0.35}'),
 ('stats','token','{"version":2,"basis":"account_usage","token_basis":"account_stats","cost_multiplier":0.35}'),
 ('per-call','per_request','{"version":2,"basis":"account_usage","cost_multiplier":0.35}')) v(label,mode,snap)`, f.user, f.key, f.account, from.Add(time.Minute))
	require.NoError(t, err)
	check(6, 222, 95, 127) // 14.50 + 35 + 35 + 5.25 + 5.25
	// Simulate a slice written by a pre-migration worker. Whole and partial
	// windows must agree, including mixed bases within one model and group.
	_, err = integrationDB.Exec("UPDATE account_usage_financial_rollups SET token_basis_projected=NULL, standard_token_amount=NULL WHERE bucket=$1 AND account_id=$2", from, f.account)
	require.NoError(t, err)
	check(6, 222, 95, 127)
	filter.From = from.Add(time.Minute)
	check(6, 222, 95, 127)
	filter.From = from
	_, err = integrationDB.Exec(`UPDATE accounts SET extra='{"cost_multiplier":0.25}' WHERE id=$1`, f.account)
	require.NoError(t, err)
	check(6, 222, 54.75, 167.25) // Current basis changes only the two historical estimates.
	_, err = integrationDB.Exec(`DELETE FROM usage_logs WHERE account_id=$1 AND request_id LIKE 'token-basis-%'`, f.account)
	require.NoError(t, err)
	_, err = integrationDB.Exec("UPDATE accounts SET extra='{}' WHERE id=$1", f.account)
	require.NoError(t, err)
	check(2, 22, 2, 8) // Unpriced income is excluded from the margin.
	_, err = integrationDB.Exec(`UPDATE accounts SET type='apikey',extra='{"cost_multiplier":0.05,"image_purchase_prices":{"1K":0.005}}' WHERE id=$1`, f.account)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,account_stats_cost,account_rate_multiplier,image_count,image_size,billing_mode,created_at)
 VALUES($1,$2,$3,'image-purchase','fixture',2,1,2,1,2,'1K','image',$4)`, f.user, f.key, f.account, from.Add(time.Minute))
	require.NoError(t, err)
	check(3, 23, 2.41, 20.59) // 2 + 8 × 0.05 + 2 images × 0.005
	_, err = integrationDB.Exec(`DELETE FROM usage_logs WHERE account_id=$1 AND request_id='image-purchase'`, f.account)
	require.NoError(t, err)
	_, err = integrationDB.Exec("DELETE FROM usage_logs WHERE account_id=$1 AND request_id='one-times'", f.account)
	require.NoError(t, err)
	// Late corrections invalidate the published slice. A partial range on
	// either edge still uses the same persisted prices and half-open bounds.
	_, err = integrationDB.Exec("UPDATE usage_logs SET actual_cost=9 WHERE account_id=$1 AND request_id='financial-rollup'", f.account)
	require.NoError(t, err)
	check(1, 9, 2, 7)
	filter.From = from.Add(time.Minute)
	filter.Until = from.Add(2 * time.Minute)
	check(1, 9, 2, 7)
	filter.From = from
	filter.Until = from.Add(time.Minute)
	check(0, 0, 0, 0)
	filter.Until = from.Add(5 * time.Minute)
	_, err = integrationDB.Exec("DELETE FROM usage_logs WHERE account_id=$1 AND request_id='financial-rollup'", f.account)
	require.NoError(t, err)
	check(0, 0, 0, 0)
}
