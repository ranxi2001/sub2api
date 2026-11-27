//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProfitStandardTierRecordedBase(t *testing.T) {
	for _, tc := range []struct {
		name, tier, details string
		total               float64
		want                *float64
	}{
		{"six_times", "ultrafast", `{"free_ultrafast":true,"total_cost":10,"multiplier":0.27}`, 60, numberPtr(10)},
		{"submitted_retry", "ultrafast", `{"free_ultrafast":true,"total_cost":10,"multiplier":0.54}`, 120, numberPtr(20)},
		{"invalid_retry_units", "ultrafast", `{"free_ultrafast":true,"total_cost":10,"multiplier":0.4}`, 120, nil},
		{"three_times", "ultrafast", `{"free_ultrafast":true,"total_cost":10,"multiplier":0.27}`, 30, numberPtr(10)},
		{"fast", "priority", `{"free_fast":true,"total_cost":10,"multiplier":0.27}`, 20, numberPtr(10)},
		{"ordinary", "default", `{}`, 10, numberPtr(10)},
		{"missing_tier", "", `{}`, 10, numberPtr(10)},
		{"no_proof", "ultrafast", `{}`, 60, nil},
		{"missing_amount", "ultrafast", `{"free_ultrafast":true}`, 60, nil},
		{"wrong_amount_type", "ultrafast", `{"free_ultrafast":true,"total_cost":"10"}`, 60, nil},
		{"negative", "ultrafast", `{"free_ultrafast":true,"total_cost":-10}`, 60, nil},
		{"too_large", "ultrafast", `{"free_ultrafast":true,"total_cost":100}`, 60, nil},
		{"zero", "ultrafast", `{"free_ultrafast":true,"total_cost":0,"multiplier":0.27}`, 60, numberPtr(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var actual sql.NullFloat64
			require.NoError(t, integrationDB.QueryRow(`SELECT profit_standard_tier_base($1,$2::jsonb,$3,0.27)`, tc.total, tc.details, tc.tier).Scan(&actual))
			require.Equal(t, tc.want != nil, actual.Valid)
			if tc.want != nil {
				require.Equal(t, *tc.want, actual.Float64)
			}
		})
	}
}

func TestProfitStandardTierAccountOverrideAndFrozenSnapshot(t *testing.T) {
	f := newChannelOperationsFixture(t)
	ctx := context.Background()
	from := time.Now().UTC().Truncate(5 * time.Minute).Add(-20 * time.Minute)
	_, err := integrationDB.Exec(`UPDATE accounts SET type='apikey',extra='{"cost_multiplier":0.25}' WHERE id=$1`, f.account)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,
 account_rate_multiplier,rate_multiplier,billing_mode,service_tier,customer_cost_details,procurement_cost_snapshot,created_at)
 SELECT $1,$2,$3,'standard-tier-'||v.label,'fixture',v.amount,2.7,1,.27,'token',v.tier,v.details::jsonb,
 '{"version":2,"basis":"account_usage","token_basis":"account_stats","cost_multiplier":0.25}'::jsonb,$4
 FROM (VALUES ('six',60,'ultrafast','{"free_ultrafast":true,"total_cost":10,"multiplier":0.27}'),
 ('three',30,'ultrafast','{"free_ultrafast":true,"total_cost":10,"multiplier":0.27}'),
 ('ordinary',10,'default','{}'),('unknown',60,'ultrafast','{}'))v(label,amount,tier,details)`, f.user, f.key, f.account, from.Add(time.Minute))
	require.NoError(t, err)
	filter := service.UpstreamOperationsFilter{From: from, Until: from.Add(5 * time.Minute), AccountID: f.account,
		AccountUsage: true, Dimension: "account", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 20}
	worker := newDashboardAggregationRepositoryWithSQL(integrationDB)
	for i := 0; i < 100; i++ {
		var pending bool
		require.NoError(t, integrationDB.QueryRow(`SELECT EXISTS(SELECT 1 FROM account_usage_financial_pending WHERE bucket=$1)`, from).Scan(&pending))
		if !pending {
			break
		}
		_, err = worker.RefreshAccountUsageFinancial(ctx)
		require.NoError(t, err)
	}
	check := func(cost float64, covered, unknown int) {
		t.Helper()
		report, err := f.repo.ReadProfitOperations(ctx, filter)
		require.NoError(t, err)
		var result struct {
			Summary struct {
				Cost    float64 `json:"upstream_cost"`
				Covered int     `json:"cost_covered_requests"`
				Unknown int     `json:"unknown_cost_requests"`
			}
		}
		require.NoError(t, json.Unmarshal(report.Usage, &result))
		require.InDelta(t, cost, result.Summary.Cost, 1e-9)
		require.Equal(t, covered, result.Summary.Covered)
		require.Equal(t, unknown, result.Summary.Unknown)
	}
	check(40, 4, 0)
	_, err = integrationDB.Exec(`UPDATE accounts SET extra='{"cost_multiplier":0.9,"token_procurement_basis":"standard_tier"}' WHERE id=$1`, f.account)
	require.NoError(t, err)
	check(7.5, 3, 1)
	usage := newUsageLogRepositoryWithSQL(integrationEntClient, integrationDB)
	checkDisplay := func(start time.Time, lifetime bool, wantKnown, wantRequests int64, wantCost *float64) {
		t.Helper()
		windows, err := usage.GetAccountProcurementWindows(ctx, []int64{f.account, f.outsider}, start, filter.Until, lifetime)
		require.NoError(t, err)
		require.NotContains(t, windows, f.outsider)
		require.Contains(t, windows, f.account)
		window := windows[f.account].Window
		require.Equal(t, wantRequests, window.Requests)
		require.Equal(t, wantKnown, window.Known)
		require.Equal(t, wantCost, window.Amount)
		if lifetime {
			require.Equal(t, int64(4), windows[f.account].Lifetime.Requests)
			require.Equal(t, numberPtr(7.5), windows[f.account].Lifetime.Amount)
		}
	}
	checkDisplay(from, false, 3, 4, numberPtr(7.5))
	checkDisplay(from.Add(time.Minute), true, 3, 4, numberPtr(7.5))
	checkDisplay(from.Add(2*time.Minute), true, 0, 0, numberPtr(0))
	// Request-time .25 is retained; neither 6x nor 3x is inferred.
	filter.From = from.Add(time.Minute)
	check(7.5, 3, 1) // Direct event boundary agrees with the converted rollup.
	filter.From = from
	_, err = integrationDB.Exec(`UPDATE account_usage_financial_rollups SET standard_tier_projected=NULL,standard_tier_amount=NULL WHERE bucket=$1 AND account_id=$2`, from, f.account)
	require.NoError(t, err)
	checkDisplay(from, false, 3, 4, numberPtr(7.5))
	check(7.5, 3, 1) // Old workers trigger the bounded raw-event projection.
	_, err = integrationDB.Exec(`UPDATE accounts SET extra='{"cost_multiplier":0.9}' WHERE id=$1`, f.account)
	require.NoError(t, err)
	check(40, 4, 0) // Reverting the selected estimate never rewrites historical usage.
}

func numberPtr(v float64) *float64 { return &v }

func TestProfitStandardTierNewSnapshotDoesNotTouchNonTokens(t *testing.T) {
	f := newChannelOperationsFixture(t)
	ctx := context.Background()
	from := time.Now().UTC().Truncate(5 * time.Minute).Add(-20 * time.Minute)
	_, err := integrationDB.Exec(`UPDATE accounts SET type='apikey',extra='{"cost_multiplier":0.25,"token_procurement_basis":"standard_tier"}' WHERE id=$1`, f.account)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,
 account_stats_cost,account_rate_multiplier,rate_multiplier,billing_mode,platform,service_tier,customer_cost_details,procurement_cost_snapshot,created_at)
 SELECT $1,$2,$3,'standard-tier-new-'||v.label,'fixture',60,2.7,v.stats,v.rate,.27,v.mode,'openai','ultrafast',
 '{"free_ultrafast":true,"total_cost":10,"multiplier":0.27}'::jsonb,v.snap::jsonb,$4
 FROM (VALUES
 ('frozen','token',NULL::numeric,1,'{"version":2,"basis":"account_usage","token_basis":"standard_tier","cost_multiplier":0.25}'),
 ('zero','token',NULL::numeric,1,'{"version":2,"basis":"account_usage","token_basis":"standard_tier","cost_multiplier":0}'),
 ('per-call','per_request',8,0.5,'{"version":2,"basis":"account_usage","token_basis":"account_stats","cost_multiplier":0.25}'),
 ('gateway','token',NULL::numeric,1,'{"version":1,"basis":"total_cost","account_type":"gateway","provider":"sub2api","target_platform":"openai","cost_multiplier":0.1}'))
 v(label,mode,stats,rate,snap)`, f.user, f.key, f.account, from.Add(time.Minute))
	require.NoError(t, err)
	filter := service.UpstreamOperationsFilter{From: from, Until: from.Add(5 * time.Minute), AccountID: f.account,
		AccountUsage: true, Dimension: "account", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 20}
	worker := newDashboardAggregationRepositoryWithSQL(integrationDB)
	for i := 0; i < 100; i++ {
		var pending bool
		require.NoError(t, integrationDB.QueryRow(`SELECT EXISTS(SELECT 1 FROM account_usage_financial_pending WHERE bucket=$1)`, from).Scan(&pending))
		if !pending {
			break
		}
		_, err = worker.RefreshAccountUsageFinancial(ctx)
		require.NoError(t, err)
	}
	for _, selected := range []string{"standard_tier", "account_stats"} {
		_, err = integrationDB.Exec(`UPDATE accounts SET extra=jsonb_build_object('cost_multiplier',0.9,'token_procurement_basis',$2::text) WHERE id=$1`, f.account, selected)
		require.NoError(t, err)
		report, err := f.repo.ReadProfitOperations(ctx, filter)
		require.NoError(t, err)
		var data struct {
			Summary struct {
				Cost float64 `json:"upstream_cost"`
			}
		}
		require.NoError(t, json.Unmarshal(report.Usage, &data))
		// Frozen token .25, explicit zero, non-token debit and gateway cost remain distinct.
		require.Equal(t, 12.5, data.Summary.Cost) // 2.5 + 0 + (8*.5) + (60*.1)
	}
}
