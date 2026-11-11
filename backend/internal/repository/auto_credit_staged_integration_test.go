//go:build vipcreditintegration

package repository

import (
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestVipCreditStagedUsageRollbackAndRecovery(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := creditUser(t, db, "0")
	configureCredit(t, db, uid, "10", "2", "4")
	_, err := NewAutoCreditRepository(db).Save(ctx, uid, uid, service.AutoCreditPatch{Version: 1, NotifyEmail: boolPtr(true)})
	require.NoError(t, err)
	var key, account int64
	require.NoError(t, db.QueryRow(`INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'staged') RETURNING id`, uid, fmt.Sprintf("sk-vip-staged-%d", uid)).Scan(&key))
	require.NoError(t, db.QueryRow(`INSERT INTO accounts(name,platform,type,credentials) VALUES($1,'openai','apikey','{}') RETURNING id`, fmt.Sprintf("credit-account-%d", uid)).Scan(&account))
	cmd := service.UsageBillingCommand{RequestID: fmt.Sprintf("vip-staged-%d", uid), UserID: uid, APIKeyID: key, AccountID: account, BalanceCost: 0.25, APIKeyQuotaCost: 0.25}
	usage := service.UsageLog{RequestID: cmd.RequestID, UserID: uid, APIKeyID: key, AccountID: account, Model: "fixture-model", ActualCost: 0.25, TotalCost: 0.25, CreatedAt: time.Now().UTC()}
	repo := &usageBillingRepository{db: db}
	require.NoError(t, repo.StageUsageBilling(ctx, &cmd, &usage))
	name := fmt.Sprintf("vip_credit_failure_%d", uid)
	_, err = db.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.request_id=%s THEN RAISE EXCEPTION 'injected usage failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON usage_logs FOR EACH ROW EXECUTE FUNCTION %s()`, name, pq.QuoteLiteral(cmd.RequestID), name, name))
	require.NoError(t, err)
	cleanup := func() {
		_, e := db.ExecContext(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON usage_logs; DROP FUNCTION IF EXISTS %s()`, name, name))
		require.NoError(t, e)
	}
	t.Cleanup(cleanup)
	_, err = repo.ApplyStagedUsageBilling(ctx, service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: key})
	require.ErrorContains(t, err, "injected usage failure")
	var balance string
	var count int
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, "0.00000000", balance)
	for _, table := range []string{"auto_credit_ledger", "auto_credit_cache_outbox"} {
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM `+table+` WHERE user_id=$1`, uid).Scan(&count))
		require.Zero(t, count, "money/cache receipt must rollback together")
	}
	cleanup()
	require.Zero(t, emailReceiptCount(t, db, uid), "late usage failure must roll back mail with the ledger")
	result, err := repo.ApplyStagedUsageBilling(ctx, service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: key})
	require.NoError(t, err)
	require.True(t, result.Result.Applied)
	require.True(t, result.Result.AutoCreditIssued)
	require.Equal(t, 1, emailReceiptCount(t, db, uid))
	result, err = repo.ApplyStagedUsageBilling(ctx, service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: key})
	require.NoError(t, err)
	require.False(t, result.Result.Applied)
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, "1.75000000", balance)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM usage_logs WHERE user_id=$1`, uid).Scan(&count))
	require.Equal(t, 1, count)
	state, err := NewAutoCreditRepository(db).Get(ctx, uid, "all")
	require.NoError(t, err)
	require.Len(t, state.Entries, 1)
	require.EqualValues(t, 1, state.Entries[0].Epoch)
}
