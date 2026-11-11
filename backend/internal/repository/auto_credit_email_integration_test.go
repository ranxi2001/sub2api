//go:build vipcreditintegration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Decode through the public JSON shape so the baseline compiles and fails on
// behavior (missing default/persistence), rather than undefined new Go fields.
func TestVipCreditEmailConfigCompatibility(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := creditUser(t, db, "0")
	repo := NewAutoCreditRepository(db)
	check := func(want bool) *service.AutoCreditState {
		t.Helper()
		state, err := repo.Get(ctx, uid, "all")
		require.NoError(t, err)
		encoded, err := json.Marshal(state.Config)
		require.NoError(t, err)
		var config map[string]any
		require.NoError(t, json.Unmarshal(encoded, &config))
		require.Equal(t, want, config["notify_email"])
		return state
	}
	check(false)
	var patch service.AutoCreditPatch
	require.NoError(t, json.Unmarshal([]byte(`{"version":0,"enabled":true,"notify_email":true,"threshold":"50","amount":"300","cap":"500"}`), &patch))
	_, err := repo.Save(ctx, uid, uid, patch)
	require.NoError(t, err)
	state := check(true)
	_, err = repo.Save(ctx, uid, uid, service.AutoCreditPatch{Version: state.Config.Version, Enabled: boolPtr(false)})
	require.NoError(t, err)
	state = check(true) // Legacy clients omitting the field must preserve it.
	patch = service.AutoCreditPatch{}
	require.NoError(t, json.Unmarshal([]byte(`{"version":2,"notify_email":false,"threshold":null,"amount":null,"cap":null}`), &patch))
	_, err = repo.Save(ctx, uid, uid, patch)
	require.NoError(t, err)
	state = check(false)
	require.Nil(t, state.Config.Amount)
}

func TestVipCreditEmailCommittedLedger(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := creditUser(t, db, "0")
	var patch service.AutoCreditPatch
	require.NoError(t, json.Unmarshal([]byte(`{"version":0,"enabled":true,"notify_email":true,"threshold":"50","amount":"300","cap":"500"}`), &patch))
	repo := NewAutoCreditRepository(db)
	_, err := repo.Save(ctx, uid, uid, patch)
	require.NoError(t, err)
	var key int64
	require.NoError(t, db.QueryRow(`INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'email fixture') RETURNING id`, uid, fmt.Sprintf("sk-email-fixture-%d", uid)).Scan(&key))
	issued, err := repo.EvaluateAdmission(ctx, uid, key)
	require.NoError(t, err)
	require.True(t, issued.Issued)
	var table *string
	require.NoError(t, db.QueryRow(`SELECT to_regclass('auto_credit_email_outbox')::text`).Scan(&table))
	require.NotNil(t, table, "committed credit must have a durable mail receipt")
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM auto_credit_email_outbox o JOIN auto_credit_ledger l ON l.id=o.ledger_id WHERE l.user_id=$1`, uid).Scan(&count))
	require.Equal(t, 1, count, "admission must enqueue exactly once for the committed ledger")
	_, err = repo.EvaluateAdmission(ctx, uid, key)
	require.NoError(t, err)
	cmd := &service.UsageBillingCommand{RequestID: fmt.Sprintf("email-debit-%d", uid), APIKeyID: key, UserID: uid, BalanceCost: 290}
	applied, err := NewUsageBillingRepository(nil, db).Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, applied.AutoCreditIssued == false, "cap must still block second 300 credit while 300 pending")
	state, err := repo.Get(ctx, uid, "all")
	require.NoError(t, err)
	_, err = repo.Settle(ctx, uid, state.Entries[0].ID, uid)
	require.NoError(t, err)
	cmd.RequestID += "-after-collection"
	cmd.BalanceCost = 1
	applied, err = NewUsageBillingRepository(nil, db).Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, applied.AutoCreditIssued)
	applied, err = NewUsageBillingRepository(nil, db).Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, applied.Applied)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM auto_credit_email_outbox o JOIN auto_credit_ledger l ON l.id=o.ledger_id WHERE l.user_id=$1`, uid).Scan(&count))
	require.Equal(t, 2, count, "real debit and duplicate request must retain one receipt per ledger")
}
