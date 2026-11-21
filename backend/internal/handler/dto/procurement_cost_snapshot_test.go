package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProcurementSnapshotAdminDTOAndUserPrivacy(t *testing.T) {
	factor := .7
	log := &service.UsageLog{TotalCost: 10, ActualCost: 3, ProcurementCostSnapshot: &service.ProcurementCostSnapshot{
		Version: 1, AccountType: "gateway", Provider: "new_api", TargetPlatform: "openai", Basis: "total_cost", CostMultiplier: &factor}}
	admin, err := json.Marshal(UsageLogFromServiceAdmin(log))
	require.NoError(t, err)
	require.Contains(t, string(admin), `"procurement_cost_snapshot"`)
	require.Contains(t, string(admin), `"cost_multiplier":0.7`)
	user, err := json.Marshal(UsageLogFromService(log))
	require.NoError(t, err)
	require.NotContains(t, string(user), "procurement_cost_snapshot")
	require.Nil(t, UsageLogFromServiceAdmin(&service.UsageLog{}).ProcurementCostSnapshot)
}
