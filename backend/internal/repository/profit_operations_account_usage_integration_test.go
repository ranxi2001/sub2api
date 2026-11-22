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
