//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// The JSON seed keeps the same test executable against the old production
// structs; a missing snapshot must fail a business assertion, not compilation.
func TestFreeFastCustomerCostPersistence(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newUsageLogRepositoryWithSQL(client, integrationDB)
	stamp := fmt.Sprint(time.Now().UnixNano())
	user := mustCreateUser(t, client, &service.User{Email: "cost-" + stamp + "@example.invalid"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "synthetic-cost-" + stamp, Name: "Fixture"})
	account := mustCreateAccount(t, client, &service.Account{Name: "Fixture", Platform: "openai"})
	t.Cleanup(func() {
		for _, row := range []struct {
			sql string
			id  int64
		}{
			{"DELETE FROM usage_logs WHERE user_id=$1", user.ID}, {"DELETE FROM api_keys WHERE id=$1", key.ID},
			{"DELETE FROM accounts WHERE id=$1", account.ID}, {"DELETE FROM users WHERE id=$1", user.ID},
		} {
			_, err := integrationDB.ExecContext(ctx, row.sql, row.id)
			require.NoError(t, err)
		}
	})
	const snapshot = `{"version":1,"free_fast":true,"input_cost":0.00071,"output_cost":0.00095,"image_input_cost":0,"image_output_cost":0,"cache_creation_cost":0,"cache_read_cost":0,"total_cost":0.00166,"actual_cost":0.000996,"multiplier":0.6}`
	for _, mode := range []string{"single", "batch", "best-effort-single", "best-effort-batch", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: "cost-" + stamp + mode,
				Model: "synthetic-astra", InputTokens: 71, OutputTokens: 19, InputCost: .00142, OutputCost: .0019, TotalCost: .00332, ActualCost: .000996, RateMultiplier: .6, CreatedAt: time.Now().UTC()}
			if mode != "legacy" {
				require.NoError(t, json.Unmarshal([]byte(`{"CustomerCostDetails":`+snapshot+`}`), log))
			}
			prepared := prepareUsageLogInsert(log)
			var err error
			switch mode {
			case "single", "legacy":
				_, err = repo.createSingle(ctx, integrationDB, log)
			case "batch":
				k := usageLogBatchKey(log.RequestID, log.APIKeyID)
				_, _, _, err = repo.batchInsertUsageLogs(integrationDB, []string{k}, map[string]usageLogInsertPrepared{k: prepared})
			case "best-effort-single":
				err = execUsageLogInsertNoResult(ctx, integrationDB, prepared)
			case "best-effort-batch":
				q, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
				_, err = integrationDB.ExecContext(ctx, q, args...)
			}
			require.NoError(t, err)
			stored, err := scanUsageLog(integrationDB.QueryRowContext(ctx, "SELECT "+usageLogSelectColumns+" FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", log.RequestID, key.ID))
			require.NoError(t, err)
			raw, err := json.Marshal(stored)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &fields))
			if mode == "legacy" {
				require.True(t, len(fields["CustomerCostDetails"]) == 0 || string(fields["CustomerCostDetails"]) == "null")
				return
			}
			require.NotEmpty(t, fields["CustomerCostDetails"], "stored usage must retain the customer Standard quote")
			require.JSONEq(t, snapshot, string(fields["CustomerCostDetails"]))
			require.InDelta(t, .00332, stored.TotalCost, 1e-10)
			require.InDelta(t, .000996, stored.ActualCost, 1e-10)
			// Repeated insertion must retain the first snapshot, not rewrite history.
			require.NoError(t, json.Unmarshal([]byte(`{"CustomerCostDetails":{"version":99}}`), log))
			_, err = repo.createSingle(ctx, integrationDB, log)
			require.NoError(t, err)
			var saved string
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT customer_cost_details::text FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", log.RequestID, key.ID).Scan(&saved))
			require.JSONEq(t, snapshot, saved)
		})
	}
}

func TestFreeFastCustomerCostDurableReplay(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	cmd.BalanceCost, cmd.APIKeyQuotaCost = .000996, .000996
	usage.InputCost, usage.OutputCost, usage.TotalCost, usage.ActualCost = .00142, .0019, .00332, .000996
	const snapshot = `{"version":1,"free_fast":true,"input_cost":0.00071,"output_cost":0.00095,"image_input_cost":0,"image_output_cost":0,"cache_creation_cost":0,"cache_read_cost":0,"total_cost":0.00166,"actual_cost":0.000996,"multiplier":0.6}`
	require.NoError(t, json.Unmarshal([]byte(`{"CustomerCostDetails":`+snapshot+`}`), &usage))
	require.NoError(t, repo.StageUsageBilling(ctx, &cmd, &usage))
	// A later retry sees different config/display data; the first event wins.
	require.NoError(t, json.Unmarshal([]byte(`{"CustomerCostDetails":{"version":99}}`), &usage))
	require.NoError(t, repo.StageUsageBilling(ctx, &cmd, &usage))
	key := service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID}
	for i := 0; i < 2; i++ {
		_, err := repo.ApplyStagedUsageBilling(ctx, key)
		require.NoError(t, err)
	}
	var saved string
	require.NoError(t, repo.db.QueryRowContext(ctx, "SELECT customer_cost_details::text FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", cmd.RequestID, cmd.APIKeyID).Scan(&saved))
	require.JSONEq(t, snapshot, saved)
	var balance, quota string
	require.NoError(t, repo.db.QueryRowContext(ctx, "SELECT balance::text FROM users WHERE id=$1", cmd.UserID).Scan(&balance))
	require.NoError(t, repo.db.QueryRowContext(ctx, "SELECT quota_used::text FROM api_keys WHERE id=$1", cmd.APIKeyID).Scan(&quota))
	require.Equal(t, "99.99900400", balance)
	require.Equal(t, "0.00099600", quota)
}

func TestFreeFastCustomerCostMigration(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// A connection-local synthetic old table makes the upgrade test independent
	// of deployed data and the migration harness's already-current schema.
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE usage_logs (id bigint, total_cost numeric, actual_cost numeric) ON COMMIT DROP;
 INSERT INTO usage_logs VALUES(1,0.00332,0.000996)`)
	require.NoError(t, err)
	sql, err := migrations.FS.ReadFile("379_usage_customer_cost_details.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.ExecContext(ctx, string(sql))
		require.NoError(t, err)
	}
	var unchanged bool
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT customer_cost_details IS NULL AND total_cost=0.00332 AND actual_cost=0.000996 FROM usage_logs WHERE id=1`).Scan(&unchanged))
	require.True(t, unchanged, "migration must leave old charges intact with unknown display evidence")
}

func TestFreeFastCustomerCostCachePersistence(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	const snapshot = `{"version":2,"free_fast":true,"input_cost":0.08345,"output_cost":0.0068,"image_input_cost":0,"image_output_cost":0,"cache_creation_cost":0.0004625,"cache_read_cost":0.000017,"total_cost":0.0907295,"actual_cost":0.04536475,"multiplier":0.5,"cache_cost_details":[{"kind":"write_30m","tokens":11,"unit_price":0.0000125},{"kind":"write_12h","tokens":13,"unit_price":0.000025},{"kind":"read","tokens":17,"unit_price":0.000001}]}`
	cmd.BalanceCost, cmd.APIKeyQuotaCost = .04536475, .04536475
	usage.TotalCost, usage.ActualCost = .2704005, .04536475
	require.NoError(t, json.Unmarshal([]byte(`{"CustomerCostDetails":`+snapshot+`}`), &usage))
	require.NoError(t, repo.StageUsageBilling(ctx, &cmd, &usage))
	key := service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID}
	for i := 0; i < 2; i++ {
		_, err := repo.ApplyStagedUsageBilling(ctx, key)
		require.NoError(t, err)
	}
	stored, err := scanUsageLog(repo.db.QueryRowContext(ctx, "SELECT "+usageLogSelectColumns+" FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", cmd.RequestID, cmd.APIKeyID))
	require.NoError(t, err)
	raw, err := json.Marshal(stored)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	require.JSONEq(t, snapshot, string(fields["CustomerCostDetails"]), "cache prices must survive durable SQL storage and readback")
	require.InDelta(t, .2704005, stored.TotalCost, 1e-10)
	require.InDelta(t, .04536475, stored.ActualCost, 1e-10)
	var quota string
	require.NoError(t, repo.db.QueryRowContext(ctx, "SELECT quota_used::text FROM api_keys WHERE id=$1", cmd.APIKeyID).Scan(&quota))
	require.Equal(t, "0.04536475", quota, "display cache prices cannot change or repeat the command")
}

func TestFreeUltrafastCustomerCostDurableReplay(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	cmd.BalanceCost, cmd.APIKeyQuotaCost = .000996, .000996
	usage.InputCost, usage.OutputCost, usage.TotalCost, usage.ActualCost = .00426, .0057, .00996, .000996
	const snapshot = `{"version":1,"free_fast":false,"free_ultrafast":true,"input_cost":0.00071,"output_cost":0.00095,"image_input_cost":0,"image_output_cost":0,"cache_creation_cost":0,"cache_read_cost":0,"total_cost":0.00166,"actual_cost":0.000996,"multiplier":0.6}`
	require.NoError(t, json.Unmarshal([]byte(`{"CustomerCostDetails":`+snapshot+`}`), &usage))
	require.NoError(t, repo.StageUsageBilling(ctx, &cmd, &usage))
	// A later retry sees different config/display data; the first event wins.
	require.NoError(t, json.Unmarshal([]byte(`{"CustomerCostDetails":{"version":99}}`), &usage))
	require.NoError(t, repo.StageUsageBilling(ctx, &cmd, &usage))
	key := service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID}
	for i := 0; i < 2; i++ {
		_, err := repo.ApplyStagedUsageBilling(ctx, key)
		require.NoError(t, err)
	}
	var saved string
	require.NoError(t, repo.db.QueryRowContext(ctx, "SELECT customer_cost_details::text FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", cmd.RequestID, cmd.APIKeyID).Scan(&saved))
	require.JSONEq(t, snapshot, saved)
	var balance, quota string
	require.NoError(t, repo.db.QueryRowContext(ctx, "SELECT balance::text FROM users WHERE id=$1", cmd.UserID).Scan(&balance))
	require.NoError(t, repo.db.QueryRowContext(ctx, "SELECT quota_used::text FROM api_keys WHERE id=$1", cmd.APIKeyID).Scan(&quota))
	require.Equal(t, "99.99900400", balance)
	require.Equal(t, "0.00099600", quota)
}

func TestAccountProbePenaltyDurableReplay(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	cmd.BalanceCost, cmd.APIKeyQuotaCost = .001, .001
	usage.InputTokens, usage.OutputTokens = 10, 13
	usage.InputCost, usage.OutputCost, usage.TotalCost, usage.ActualCost = .00003, .000195, .000225, .001
	usage.CustomerCostDetails = &service.CustomerCostDetails{Version: 4, ProbePenalty: &service.ProbePenaltyReceipt{MinimumCharge: .001, Adjustment: .0007525, ActualCost: .001, OriginalOutputTokens: 13, BillingOutputTokens: 59, OutputTokenPrice: .0000165}}
	require.NoError(t, repo.StageUsageBilling(ctx, &cmd, &usage))
	key := service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID}
	for i := 0; i < 2; i++ {
		recovered, err := repo.ApplyStagedUsageBilling(ctx, key)
		require.NoError(t, err)
		require.True(t, recovered.Settled)
		require.Equal(t, .001, recovered.Event.Usage.CustomerCostDetails.ProbePenalty.ActualCost)
		require.Equal(t, 13, recovered.Event.Usage.OutputTokens)
	}
	var balance, quota string
	require.NoError(t, repo.db.QueryRowContext(ctx, "SELECT balance::text FROM users WHERE id=$1", cmd.UserID).Scan(&balance))
	require.NoError(t, repo.db.QueryRowContext(ctx, "SELECT quota_used::text FROM api_keys WHERE id=$1", cmd.APIKeyID).Scan(&quota))
	require.Equal(t, "99.99900000", balance)
	require.Equal(t, "0.00100000", quota)
}
