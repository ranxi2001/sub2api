//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestUpstreamOperationsPersistedProfitIndependentFromAccountDebit(t *testing.T) {
	f := newChannelOperationsFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	_, err := integrationDB.Exec(`UPDATE accounts SET type='apikey',credentials='{"base_url":"HTTPS://RELAY.EXAMPLE:8443/tenant/v1/","api_key":"fixture-secret"}',extra='{"cost_multiplier":0.25}',rate_multiplier=99 WHERE id=$1`, f.account)
	require.NoError(t, err)
	for i, v := range []struct{ base, income, stats float64 }{{10, 4, 8}, {2, 0, 4}, {0, 0, 0}} {
		_, err = integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,account_stats_cost,account_rate_multiplier,created_at) VALUES($1,$2,$3,$4,'fixture-model',$5,$6,$7,99,$8)`, f.user, f.key, f.account, "profit-fixture-"+now.Format("150405")+string(rune('a'+i)), v.base, v.income, v.stats, now.Add(-time.Hour))
		require.NoError(t, err)
	}
	q := service.UpstreamOperationsFilter{From: now.Add(-24 * time.Hour), Until: now, AccountID: f.account, Dimension: "upstream", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 1}
	out, err := f.repo.ReadUpstreamOperations(context.Background(), q)
	require.NoError(t, err)
	var data struct {
		Summary struct {
			Requests int      `json:"requests"`
			Standard float64  `json:"standard_charge"`
			Income   float64  `json:"request_income"`
			Cost     *float64 `json:"estimated_cost"`
			Profit   *float64 `json:"estimated_profit"`
			Margin   *float64 `json:"margin"`
		}
		Rows []struct {
			Key string `json:"key"`
		}
		Total int
	}
	require.NoError(t, json.Unmarshal(out.Usage, &data))
	require.Equal(t, 3, data.Summary.Requests, "automatically attributed usage must not need model-channel bindings")
	require.Equal(t, 12.0, data.Summary.Standard)
	require.Equal(t, 4.0, data.Summary.Income)
	require.NotNil(t, data.Summary.Cost)
	require.InDelta(t, 3, *data.Summary.Cost, 1e-9, "cost uses saved stats base × independent cost multiplier, never account debit 99")
	require.NotNil(t, data.Summary.Profit)
	require.InDelta(t, 1, *data.Summary.Profit, 1e-9)
	require.NotNil(t, data.Summary.Margin)
	require.InDelta(t, .25, *data.Summary.Margin, 1e-9, "margin uses total amounts")
	require.Len(t, data.Rows, 1)
	require.Equal(t, "https://relay.example:8443/tenant/v1", data.Rows[0].Key)
	require.NotContains(t, string(out.Usage), "fixture-secret")
}

func TestUpstreamOperationsZeroLossUnknownAndCurrentConfiguration(t *testing.T) {
	f := newChannelOperationsFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	_, err := integrationDB.Exec(`UPDATE accounts SET type='apikey',credentials='{"base_url":"https://source.example/tenant/v1"}',extra='{"cost_multiplier":0.5}' WHERE id=$1`, f.account)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,account_stats_cost,created_at) VALUES($1,$2,$3,'loss-fixture','model',10,0,8,$4)`, f.user, f.key, f.account, now.Add(-time.Hour))
	require.NoError(t, err)
	q := service.UpstreamOperationsFilter{From: now.Add(-24 * time.Hour), Until: now, AccountID: f.account, Dimension: "account", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 20}
	read := func() map[string]any {
		out, e := f.repo.ReadUpstreamOperations(context.Background(), q)
		require.NoError(t, e)
		var data map[string]any
		require.NoError(t, json.Unmarshal(out.Usage, &data))
		return data
	}
	data := read()
	sum := data["summary"].(map[string]any)
	require.Equal(t, 4.0, sum["estimated_cost"])
	require.Equal(t, -4.0, sum["estimated_profit"])
	require.Nil(t, sum["margin"], "zero revenue margin is unknown")
	for _, tc := range []struct {
		extra    string
		cost     any
		defaults float64
		unknown  float64
	}{
		{`{"cost_multiplier":0}`, 0.0, 0, 0}, {`{}`, 0.8, 1, 0}, {`{"cost_multiplier":"bad"}`, nil, 0, 1}, {`{"cost_multiplier":-1}`, nil, 0, 1},
	} {
		_, err = integrationDB.Exec(`UPDATE accounts SET extra=$2::jsonb,updated_at=clock_timestamp() WHERE id=$1`, f.account, tc.extra)
		require.NoError(t, err)
		data = read()
		sum = data["summary"].(map[string]any)
		require.Equal(t, tc.cost, sum["estimated_cost"])
		require.Equal(t, tc.defaults, sum["default_multiplier_requests"])
		require.Equal(t, tc.unknown, sum["unknown_cost_requests"])
	}
	_, err = integrationDB.Exec(`UPDATE accounts SET credentials='{"base_url":"https://new.example/different/v1"}',extra='{"cost_multiplier":0.25}',deleted_at=NOW() WHERE id=$1`, f.account)
	require.NoError(t, err)
	data = read()
	sum = data["summary"].(map[string]any)
	require.Equal(t, 1.0, sum["deleted_account_requests"])
	require.Equal(t, 2.0, sum["estimated_cost"])
	var base, income, stats float64
	require.NoError(t, integrationDB.QueryRow(`SELECT total_cost,actual_cost,account_stats_cost FROM usage_logs WHERE account_id=$1`, f.account).Scan(&base, &income, &stats))
	require.Equal(t, 10.0, base)
	require.Zero(t, income)
	require.Equal(t, 8.0, stats, "configuration edits never rewrite history")
}

func TestUpstreamOperationsHalfOpenPagingGroupsAndPathIsolation(t *testing.T) {
	f := newChannelOperationsFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	from := now.Add(-24 * time.Hour)
	for _, a := range []struct {
		id   int64
		base string
	}{{f.account, "https://same.example:8443/A/v1/"}, {f.outsider, "https://same.example:8443/B/v1/"}} {
		_, err := integrationDB.Exec(`UPDATE accounts SET type='apikey',credentials=jsonb_build_object('base_url',$2::text),extra='{"cost_multiplier":0}' WHERE id=$1`, a.id, a.base)
		require.NoError(t, err)
		for i, at := range []time.Time{from, now, from.Add(-time.Microsecond)} {
			_, err = integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,group_id,created_at) VALUES($1,$2,$3,$4,'per-request',2,1,$5,$6)`, f.user, f.key, a.id, fmt.Sprintf("range-%d-%d", a.id, i), f.group, at)
			require.NoError(t, err)
		}
	}
	q := service.UpstreamOperationsFilter{From: from, Until: now, GroupID: f.group, Dimension: "upstream", ModelScope: "public", Sort: "name", Direction: "asc", Page: 1, PageSize: 1}
	read := func() map[string]any {
		out, err := f.repo.ReadUpstreamOperations(context.Background(), q)
		require.NoError(t, err)
		var data map[string]any
		require.NoError(t, json.Unmarshal(out.Usage, &data))
		require.Equal(t, "ready", out.Errors.Status)
		if q.Model != "" && q.ModelScope != "upstream" {
			require.Equal(t, "unsupported_scope", out.Quality.Status)
		} else {
			require.Equal(t, "ready", out.Quality.Status)
		}
		return data
	}
	first := read()
	require.Equal(t, 2.0, first["total"])
	require.Equal(t, 2.0, first["summary"].(map[string]any)["requests"])
	require.Len(t, first["rows"], 1)
	require.Equal(t, "https://same.example:8443/A/v1", first["rows"].([]any)[0].(map[string]any)["key"])
	require.Equal(t, 1.0, first["summary"].(map[string]any)["margin"], "zero procurement cost remains an explicit 100 percent margin")
	q.Page = 2
	second := read()
	require.Equal(t, first["summary"], second["summary"])
	require.Equal(t, "https://same.example:8443/B/v1", second["rows"].([]any)[0].(map[string]any)["key"])
	q.Model = "nonexistent"
	empty := read()
	require.Equal(t, 0.0, empty["summary"].(map[string]any)["requests"])
}

func TestUpstreamOperationsQualityRetainsCompleteFrozenRoundBoundary(t *testing.T) {
	f := newChannelOperationsFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	_, err := integrationDB.Exec(`UPDATE accounts SET type='apikey',credentials='{"base_url":"https://quality.example/v1"}' WHERE id=$1`, f.account)
	require.NoError(t, err)
	var plan int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config) VALUES($1,'current-model','*/30 * * * *',false,'{"quality":{}}') RETURNING id`, f.account).Scan(&plan))
	_, err = integrationDB.Exec(`INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message) VALUES($1,'success',$2,$2,$2,'{"quality":{},"model_id":"a","model_ids":["a","b"],"parallel_count":1}','frozen-peer','passed','{"verdict":"correct"}','')`, plan, now.Add(-time.Minute))
	require.NoError(t, err)
	q := service.UpstreamOperationsFilter{From: now.Add(-time.Hour), Until: now, Upstream: "https://quality.example/v1", AccountID: f.account, Model: "a", ModelScope: "upstream", Dimension: "model", Sort: "requests", Direction: "desc", Page: 1, PageSize: 20}
	out, err := f.repo.ReadUpstreamOperations(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, "ready", out.Quality.Status)
	var quality struct {
		Summary struct{ Passed, Inconclusive int }
		Rows    []struct {
			Complete bool `json:"round_complete"`
			Outcome  string
		}
	}
	require.NoError(t, json.Unmarshal(out.Quality.Data, &quality))
	require.Zero(t, quality.Summary.Passed, "missing peer still cannot pass after changing upstream cohort")
	require.Equal(t, 1, quality.Summary.Inconclusive)
	require.Len(t, quality.Rows, 1)
	require.False(t, quality.Rows[0].Complete)
}

func TestUpstreamOperationsUnknownURLNoSecretsAndInvalidAmounts(t *testing.T) {
	f := newChannelOperationsFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	_, err := integrationDB.Exec(`UPDATE accounts SET type='apikey',credentials='{"base_url":"https://user:private@secret.example/v1?token=private#private","api_key":"not-for-response"}',extra='{"cost_multiplier":0.25}' WHERE id=$1`, f.account)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,account_stats_cost,created_at) VALUES($1,$2,$3,'unknown-fixture','missing-price',10,2,'NaN'::numeric,$4)`, f.user, f.key, f.account, now.Add(-time.Minute))
	require.NoError(t, err)
	q := service.UpstreamOperationsFilter{From: now.Add(-time.Hour), Until: now, AccountID: f.account, Dimension: "upstream", ModelScope: "public", Sort: "estimated_profit", Direction: "desc", Page: 1, PageSize: 20}
	out, err := f.repo.ReadUpstreamOperations(context.Background(), q)
	require.NoError(t, err)
	var data struct {
		Summary struct {
			Cost            *float64 `json:"estimated_cost"`
			Unknown         int      `json:"unknown_cost_requests"`
			UpstreamUnknown int      `json:"unknown_upstream_requests"`
		}
		Rows []struct{ Key string }
	}
	require.NoError(t, json.Unmarshal(out.Usage, &data))
	require.Nil(t, data.Summary.Cost)
	require.Equal(t, 1, data.Summary.Unknown)
	require.Equal(t, 1, data.Summary.UpstreamUnknown)
	require.Len(t, data.Rows, 1)
	require.Equal(t, "unknown:invalid_url", data.Rows[0].Key)
	for _, secret := range []string{"private", "secret.example", "not-for-response", "NaN", "Infinity"} {
		require.NotContains(t, string(out.Usage), secret)
	}
	q.Upstream = data.Rows[0].Key
	filtered, err := f.repo.ReadUpstreamOperations(context.Background(), q)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(filtered.Usage, &data))
	require.Len(t, data.Rows, 1, "unknown attribution stays reachable by its bucket")
}
