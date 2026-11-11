package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAutoCreditBalanceHistoryAdminProjection(t *testing.T) {
	record := &service.RedeemCode{Type: service.BalanceHistoryTypeAutoCredit, Status: service.StatusUsed,
		AutoCredit: &service.AutoCreditBalanceHistory{LedgerID: 8, Amount: "0.00000001", BalanceBefore: "0.00000000", BalanceAfter: "0.00000001", Status: "pending"}}
	raw, err := json.Marshal(RedeemCodeFromServiceAdmin(record))
	require.NoError(t, err)
	require.Contains(t, string(raw), `"amount":"0.00000001"`)
	require.Contains(t, string(raw), `"ledger_id":8`)
	require.Contains(t, string(raw), `"settled_at":null`)
	public, err := json.Marshal(RedeemCodeFromService(record))
	require.NoError(t, err)
	require.NotContains(t, string(public), `"auto_credit":`)
	ordinary, err := json.Marshal(RedeemCodeFromServiceAdmin(&service.RedeemCode{Type: service.RedeemTypeBalance}))
	require.NoError(t, err)
	require.NotContains(t, string(ordinary), `"auto_credit":`)
}
