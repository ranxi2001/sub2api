//go:build integration

package repository

import (
	"context"
	"encoding/json"
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
