package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestFreeFastCustomerCostDTO(t *testing.T) {
	row := &service.UsageLog{InputCost: .00142, OutputCost: .0019, TotalCost: .00332, ActualCost: .000996}
	require.NoError(t, json.Unmarshal([]byte(`{"CustomerCostDetails":{"version":1,"free_fast":true,"input_cost":0.00071,"output_cost":0.00095,"total_cost":0.00166,"actual_cost":0.000996,"multiplier":0.6}}`), row))
	for _, mapped := range []any{UsageLogFromService(row), UsageLogFromServiceForUser(row, false), UsageLogFromServiceAdmin(row)} {
		raw, err := json.Marshal(mapped)
		require.NoError(t, err)
		var data map[string]any
		require.NoError(t, json.Unmarshal(raw, &data))
		detail, ok := data["customer_cost_details"].(map[string]any)
		require.True(t, ok, "user/admin DTO must expose the immutable customer Standard quote")
		require.InDelta(t, .00071, detail["input_cost"], 1e-12)
		require.InDelta(t, .00332, data["total_cost"], 1e-12)
		require.InDelta(t, .000996, data["actual_cost"], 1e-12)
	}
	legacy, err := json.Marshal(UsageLogFromService(&service.UsageLog{}))
	require.NoError(t, err)
	require.NotContains(t, string(legacy), "customer_cost_details")
}

func TestFreeFastCustomerCostCacheDTO(t *testing.T) {
	row := &service.UsageLog{}
	require.NoError(t, json.Unmarshal([]byte(`{"CustomerCostDetails":{"version":2,"free_fast":true,"cache_cost_details":[{"kind":"write_12h","tokens":13,"unit_price":0.000025}]}}`), row))
	for _, mapped := range []any{UsageLogFromService(row), UsageLogFromServiceForUser(row, false), UsageLogFromServiceAdmin(row)} {
		raw, err := json.Marshal(mapped)
		require.NoError(t, err)
		var data map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &data))
		var snapshot map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(data["customer_cost_details"], &snapshot))
		require.JSONEq(t, `[{"kind":"write_12h","tokens":13,"unit_price":0.000025}]`, string(snapshot["cache_cost_details"]), "customer cache price DTO parity")
	}
}
