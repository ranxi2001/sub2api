//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func durableBillingFixture(t *testing.T) (*usageBillingRepository, service.UsageBillingCommand, service.UsageLog, *dbent.Client) {
	t.Helper()
	db, client := newBillingAuditDatabase(t)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash", Balance: 100})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-mock-" + uuid.NewString(), Name: "delivery", Quota: 100})
	account := mustCreateAccount(t, client, &service.Account{Name: "delivery-" + uuid.NewString(), Type: service.AccountTypeAPIKey})
	cmd := service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, BalanceCost: 1, APIKeyQuotaCost: 1}
	usage := service.UsageLog{RequestID: cmd.RequestID, UserID: cmd.UserID, APIKeyID: cmd.APIKeyID, AccountID: cmd.AccountID,
		Model: "mock-model", InputTokens: 10, ActualCost: 1, TotalCost: 1, CreatedAt: time.Now().UTC(),
		// Relations must never enter the persistent billing envelope.
		User:   &service.User{ID: user.ID, PasswordHash: "must-not-be-persisted"},
		APIKey: &service.APIKey{Key: "must-not-be-persisted"},
	}
	return &usageBillingRepository{db: db}, cmd, usage, client
}

func TestUsageBillingDeliveryCrashBeforeExecutionAndTwoWriters(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	db := repo.db
	require.NoError(t, repo.StageUsageBilling(ctx, &cmd, &usage))
	// No execution in the first process. Only the staged event survives.
	var payload string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT payload::text FROM usage_billing_events WHERE request_id=$1 AND api_key_id=$2", cmd.RequestID, cmd.APIKeyID).Scan(&payload))
	require.NotContains(t, payload, "must-not-be-persisted")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			freshProcess := &usageBillingRepository{db: db}
			_, err := freshProcess.ApplyStagedUsageBilling(ctx, service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var balance, quota string
	var logs int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT balance::text FROM users WHERE id=$1", cmd.UserID).Scan(&balance))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT quota_used::text FROM api_keys WHERE id=$1", cmd.APIKeyID).Scan(&quota))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", cmd.RequestID, cmd.APIKeyID).Scan(&logs))
	require.Equal(t, "99.00000000", balance)
	require.Equal(t, "1.00000000", quota)
	require.Equal(t, 1, logs)
	// A restart between cache invalidation and acknowledgement repeats only
	// invalidation, not the amount. Different command ownership is rejected.
	wrong := cmd
	wrong.UserID++
	require.ErrorIs(t, repo.StageUsageBilling(ctx, &wrong, &usage), service.ErrUsageBillingRequestConflict)
}

func TestUsageBillingDeliveryLogFailureRollsBackMoneyAndRecovers(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	db := repo.db
	require.NoError(t, repo.StageUsageBilling(ctx, &cmd, &usage))
	name := "billing_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	// A real PostgreSQL failure between monetary UPDATEs and canonical INSERT.
	_, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN IF NEW.request_id='%s' THEN RAISE EXCEPTION 'injected canonical failure'; END IF; RETURN NEW; END; $$;
	CREATE TRIGGER %s BEFORE INSERT ON usage_logs FOR EACH ROW EXECUTE FUNCTION %s()`, name, cmd.RequestID, name, name))
	require.NoError(t, err)
	cleanup := func() {
		_, _ = db.ExecContext(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON usage_logs; DROP FUNCTION IF EXISTS %s()", name, name))
	}
	t.Cleanup(cleanup)
	key := service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID}
	_, err = repo.ApplyStagedUsageBilling(ctx, key)
	require.Error(t, err)
	var balance, state string
	var claims int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT balance::text FROM users WHERE id=$1", cmd.UserID).Scan(&balance))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT state FROM usage_billing_events WHERE request_id=$1 AND api_key_id=$2", cmd.RequestID, cmd.APIKeyID).Scan(&state))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2", cmd.RequestID, cmd.APIKeyID).Scan(&claims))
	require.Equal(t, "100.00000000", balance)
	require.Equal(t, "pending", state)
	require.Zero(t, claims)
	cleanup()
	result, err := (&usageBillingRepository{db: db}).ApplyStagedUsageBilling(ctx, key)
	require.NoError(t, err)
	require.True(t, result.Result.Applied)
	require.NoError(t, repo.AcknowledgeUsageBillingCache(ctx, key))
	result, err = repo.ApplyStagedUsageBilling(ctx, key)
	require.NoError(t, err)
	require.False(t, result.Result.Applied)
	require.True(t, result.Settled)
	require.False(t, result.NeedsCache)
}

func TestUsageBillingDeliveryArchiveClaimNeverBackChargesHistory(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	db := repo.db
	cmd.Normalize()
	_, err := db.ExecContext(ctx, `INSERT INTO usage_billing_dedup_archive
		(request_id,api_key_id,request_fingerprint,created_at) VALUES($1,$2,$3,NOW())`, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint)
	require.NoError(t, err)
	require.NoError(t, repo.StageUsageBilling(ctx, &cmd, &usage))
	r, err := repo.ApplyStagedUsageBilling(ctx, service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID})
	require.NoError(t, err)
	require.False(t, r.Result.Applied)
	require.False(t, r.Settled, "legacy dedup is not proof that this staged usage settled")
	var balance string
	var logs int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT balance::text FROM users WHERE id=$1", cmd.UserID).Scan(&balance))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", cmd.RequestID, cmd.APIKeyID).Scan(&logs))
	require.Equal(t, "100.00000000", balance)
	require.Zero(t, logs, "no fabricated historical canonical usage")
}
