//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingQuoteKeepsRawQuoteAndExactMoneyDelta(t *testing.T) {
	ctx := context.Background()
	db, client := newBillingAuditDatabase(t)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash", Balance: 1})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-mock-" + uuid.NewString(), Name: "quote", Quota: 1})
	account := mustCreateAccount(t, client, &service.Account{Name: "quote-" + uuid.NewString(), Type: service.AccountTypeAPIKey})
	raw := 0.000078125
	cmd := &service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID,
		BalanceCost: raw, APIKeyQuotaCost: raw, QuotedActualCost: &raw}
	repo := NewUsageBillingRepository(nil, db)
	for i := 0; i < 2; i++ {
		_, err := repo.Apply(ctx, cmd)
		require.NoError(t, err)
	}
	var balance, quota, quote, charged string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT balance::text FROM users WHERE id=$1", user.ID).Scan(&balance))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT quota_used::text FROM api_keys WHERE id=$1", key.ID).Scan(&quota))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT quoted_actual_cost::text,canonical_actual_cost::text FROM usage_billing_quotes WHERE request_id=$1 AND api_key_id=$2", cmd.RequestID, key.ID).Scan(&quote, &charged))
	require.Equal(t, "0.99992187", balance)
	require.Equal(t, "0.00007813", quota)
	require.Equal(t, "0.000078125", quote)
	require.Equal(t, "0.00007813", charged)

	insertUsage := func(request string) (id int64) {
		t.Helper()
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,actual_cost) VALUES($1,$2,$3,$4,'fixture',$5) RETURNING id`, user.ID, key.ID, account.ID, request, raw).Scan(&id))
		return
	}
	id := insertUsage(cmd.RequestID)
	// A positive Simple Mode/API-key quota quote is NOT a wallet debit.
	quotaOnly := &service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, APIKeyQuotaCost: raw, QuotedActualCost: &raw}
	_, err := repo.Apply(ctx, quotaOnly)
	require.NoError(t, err)
	ids := []int64{id, insertUsage(quotaOnly.RequestID), insertUsage("historic-unproven")}
	reader := &usageLogRepository{sql: db}
	debits, err := reader.ReadUsageSettledDebits(ctx, user.ID, ids)
	require.NoError(t, err)
	require.Equal(t, []service.UsageSettledDebit{{UsageID: id, UserID: user.ID, APIKeyID: key.ID, RequestID: cmd.RequestID, Amount: "0.00007813", Currency: "USD"}}, debits)
	other, err := reader.ReadUsageSettledDebits(ctx, user.ID+1, ids)
	require.NoError(t, err)
	require.Empty(t, other)
}

func TestUsageBillingRawQuoteBoundarySurvivesStorageWithoutDoubleRounding(t *testing.T) {
	for _, tc := range []struct {
		raw              float64
		quote, canonical string
	}{
		{0.00000000495, "0.00000000495", "0.00000000"},
		{0.00000000505, "0.00000000505", "0.00000001"},
	} {
		ctx := context.Background()
		db, client := newBillingAuditDatabase(t)
		user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash", Balance: 1})
		key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-mock-" + uuid.NewString(), Name: "quote-boundary", Quota: 1})
		cmd := &service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID,
			BalanceCost: tc.raw, APIKeyQuotaCost: tc.raw, QuotedActualCost: &tc.raw}
		_, err := NewUsageBillingRepository(nil, db).Apply(ctx, cmd)
		require.NoError(t, err)
		var quote, canonical string
		var version int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT quoted_actual_cost::text,canonical_actual_cost::text,quote_encoding_version
			FROM usage_billing_quotes WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, key.ID).Scan(&quote, &canonical, &version))
		require.Equal(t, tc.quote, quote)
		require.Equal(t, tc.canonical, canonical)
		require.Equal(t, 1, version)
		var quota string
		require.NoError(t, db.QueryRowContext(ctx, "SELECT quota_used::text FROM api_keys WHERE id=$1", key.ID).Scan(&quota))
		require.Equal(t, tc.canonical, quota, "canonical receipt is the executed command, not a re-rounded stored quote")
	}
}
