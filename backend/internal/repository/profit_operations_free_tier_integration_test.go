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

func TestProfitOperationsFreeTierAccountStats(t *testing.T) {
	f := newChannelOperationsFixture(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(5 * time.Minute).Add(-20 * time.Minute)
	end := start.Add(5 * time.Minute)
	_, err := integrationDB.Exec(`INSERT INTO usage_logs
 (user_id,api_key_id,account_id,request_id,model,created_at,total_cost,actual_cost,rate_multiplier,account_rate_multiplier,service_tier,customer_cost_details,procurement_cost_snapshot)
 SELECT $1,$2,$3,'free-tier-'||label,'fixture',$4,tier_base,income,0.27,1,tier,receipt::jsonb,
 '{"version":2,"basis":"account_usage","token_basis":"account_stats","cost_multiplier":0.25}'::jsonb
 FROM (VALUES
 ('fast',20,2.7,'priority','{"free_fast":true,"total_cost":10,"multiplier":0.27}'),
 ('ultra',60,2.7,'ultrafast','{"free_ultrafast":true,"total_cost":10,"multiplier":0.27}'),
 ('retry',120,5.4,'ultrafast','{"free_ultrafast":true,"total_cost":10,"multiplier":0.54}'),
 ('paid',30,8.1,'ultrafast',NULL),
 ('ordinary',10,2.7,'default',NULL)) v(label,tier_base,income,tier,receipt)`, f.user, f.key, f.account, start.Add(time.Minute))
	require.NoError(t, err)

	repo := newUsageLogRepositoryWithSQL(integrationEntClient, integrationDB)
	checkAccount := func() {
		t.Helper()
		usage, err := repo.GetAccountUsageStats(ctx, f.account, start, end)
		require.NoError(t, err)
		require.Equal(t, 80.0, usage.Summary.TotalCost)
		require.InDelta(t, 21.6, usage.Summary.TotalUserCost, 1e-8)
		require.Equal(t, 240.0, usage.Summary.TotalStandardCost)
		require.Len(t, usage.Models, 1)
		require.Equal(t, 80.0, usage.Models[0].AccountCost)
	}
	checkAccount() // The dialog can read raw events while the bucket is pending.
	worker := newDashboardAggregationRepositoryWithSQL(integrationDB)
	for i := 0; i < 100; i++ {
		var pending bool
		require.NoError(t, integrationDB.QueryRow("SELECT EXISTS(SELECT 1 FROM account_usage_financial_pending WHERE bucket=$1)", start).Scan(&pending))
		if !pending {
			break
		}
		_, err = worker.RefreshAccountUsageFinancial(ctx)
		require.NoError(t, err)
	}
	checkProfit := func() {
		t.Helper()
		report, err := f.repo.ReadProfitOperations(ctx, service.UpstreamOperationsFilter{From: start, Until: end, AccountID: f.account, AccountUsage: true, Dimension: "account", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 20})
		require.NoError(t, err)
		var data struct {
			Summary struct {
				Cost   float64 `json:"upstream_cost"`
				Income float64 `json:"request_income"`
			}
		}
		require.NoError(t, json.Unmarshal(report.Usage, &data))
		require.Equal(t, 20.0, data.Summary.Cost)
		require.InDelta(t, 21.6, data.Summary.Income, 1e-8)
	}
	checkAccount()
	checkProfit()
	// A published pre-upgrade bucket must not leak its tier surcharge before
	// the background worker reaches it.
	_, err = integrationDB.Exec("UPDATE account_usage_financial_rollups SET account_charge=240,procurement_amount=60 WHERE account_id=$1 AND bucket=$2", f.account, start)
	require.NoError(t, err)
	_, err = integrationDB.Exec("INSERT INTO account_profit_legacy_buckets VALUES($1) ON CONFLICT DO NOTHING", start)
	require.NoError(t, err)
	checkAccount()
	checkProfit()
}
