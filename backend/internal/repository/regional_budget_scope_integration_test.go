//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/regional/ledger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRegionalBudgetScopeReservesEveryDimension(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	for _, dimension := range []string{"key", "key_rate", "account", "platform", "subscription"} {
		t.Run(dimension, func(t *testing.T) {
			user := regionalLedgerUser(t)
			t.Cleanup(func() { _, _ = integrationDB.Exec("DELETE FROM regional_budget_allocations WHERE user_id=$1", user) })
			g := &service.Group{Name: "scope-" + uuid.NewString(), Platform: service.PlatformOpenAI, RateMultiplier: 1}
			if dimension == "subscription" {
				g.SubscriptionType = "subscription"
				limit := 7.0
				g.DailyLimitUSD = &limit
			}
			group := mustCreateGroup(t, client, g)
			key := mustCreateApiKey(t, client, &service.APIKey{UserID: user, GroupID: &group.ID, Key: "sk-mock-" + uuid.NewString(), Name: "scope"})
			account := mustCreateAccount(t, client, &service.Account{Name: "scope-" + uuid.NewString(), Type: service.AccountTypeAPIKey, Platform: service.PlatformOpenAI, Status: "active", Schedulable: true})
			_, err := integrationDB.Exec("INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)", account.ID, group.ID)
			require.NoError(t, err)
			switch dimension {
			case "key":
				_, err = integrationDB.Exec("UPDATE api_keys SET quota=7 WHERE id=$1", key.ID)
			case "key_rate":
				_, err = integrationDB.Exec("UPDATE api_keys SET rate_limit_5h=7 WHERE id=$1", key.ID)
			case "account":
				_, err = integrationDB.Exec("UPDATE accounts SET extra=jsonb_build_object('quota_daily_limit',7) WHERE id=$1", account.ID)
			case "platform":
				_, err = integrationDB.Exec("INSERT INTO user_platform_quotas(user_id,platform,daily_limit_usd) VALUES($1,'openai',7)", user)
			case "subscription":
				_, err = integrationDB.Exec("INSERT INTO user_subscriptions(user_id,group_id,starts_at,expires_at,status) VALUES($1,$2,NOW()-interval '1 minute',NOW()+interval '1 day','active')", user, group.ID)
			}
			require.NoError(t, err)
			primary, _, _ := NewRegionalFundsAuthorities(integrationDB, func(context.Context, *sql.Tx) error { return nil })
			first := regionalLedgerLease(fmt.Sprintf("scope-first-%d", user), user, 4_000_000_000)
			first.APIKeyID = key.ID
			first.AccountID = account.ID
			if dimension == "subscription" {
				first.BillingMode = "subscription"
			}
			require.NoError(t, primary.Issue(ctx, first))
			second := first
			second.LeaseID = fmt.Sprintf("scope-second-%d", user)
			second.RegionID = "eu"
			second.IssuedAt = time.Now().Add(-time.Second).Truncate(time.Microsecond)
			second.ExpiresAt = second.IssuedAt.Add(time.Hour)
			require.ErrorIs(t, primary.Issue(ctx, second), ledger.ErrExhausted)
			var balance string
			require.NoError(t, integrationDB.QueryRow("SELECT balance::text FROM users WHERE id=$1", user).Scan(&balance))
			if dimension == "subscription" {
				require.Equal(t, "10.00000000", balance)
			} else {
				require.Equal(t, "6.00000000", balance)
			}
		})
	}
}

func TestRegionalSubscriptionCanonicalUsesDurableFundingMetadata(t *testing.T) {
	ctx := context.Background()
	user := regionalLedgerUser(t)
	client := testEntClient(t)
	group := mustCreateGroup(t, client, &service.Group{Name: "sub-canonical-" + uuid.NewString(), Platform: service.PlatformOpenAI, SubscriptionType: service.SubscriptionTypeSubscription, RateMultiplier: 1})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user, GroupID: &group.ID, Key: "sk-mock-" + uuid.NewString(), Name: "sub-canonical"})
	account := mustCreateAccount(t, client, &service.Account{Name: "sub-canonical-" + uuid.NewString(), Type: service.AccountTypeAPIKey, Platform: service.PlatformOpenAI, Status: "active", Schedulable: true})
	_, err := integrationDB.Exec("INSERT INTO account_groups(account_id,group_id)VALUES($1,$2)", account.ID, group.ID)
	require.NoError(t, err)
	var subscription int64
	require.NoError(t, integrationDB.QueryRow("INSERT INTO user_subscriptions(user_id,group_id,starts_at,expires_at,status) VALUES($1,$2,NOW()-interval '1 hour',NOW()+interval '30 days','active')RETURNING id", user, group.ID).Scan(&subscription))
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("DELETE FROM usage_logs WHERE user_id=$1", user)
		_, _ = integrationDB.Exec("DELETE FROM regional_primary_requests WHERE user_id=$1", user)
		_, _ = integrationDB.Exec("DELETE FROM regional_budget_allocations WHERE user_id=$1", user)
	})
	_, holds, _ := NewRegionalFundsAuthorities(integrationDB, func(context.Context, *sql.Tx) error { return nil })
	price := ledger.Price{Model: "mock-subscription", Input: 1_000_000_000, Output: 2_000_000_000, MaxInput: 100, MaxOutput: 100}
	require.NoError(t, ledger.InstallPrice(ctx, integrationDB, price))
	lease := regionalLedgerLease("not-used", user, 1_000_000_000)
	request := regionalLedgerReservation(lease, "sub-"+uuid.NewString(), 1_000_000_000)
	request.LeaseID = ""
	request.APIKeyID = key.ID
	request.AccountID = account.ID
	request.Model = price.Model
	request.PricingVersion = price.Version()
	_, err = holds.Reserve(ctx, request)
	require.NoError(t, err)
	require.NoError(t, holds.Start(ctx, request.RequestID))
	// Mid-flight entity changes must not rewrite request-time provider or billing metadata.
	_, err = integrationDB.Exec("UPDATE accounts SET platform='anthropic' WHERE id=$1", account.ID)
	require.NoError(t, err)
	_, err = integrationDB.Exec("UPDATE groups SET platform='gemini' WHERE id=$1", group.ID)
	require.NoError(t, err)
	_, err = integrationDB.Exec("UPDATE api_keys SET group_id=NULL WHERE id=$1", key.ID)
	require.NoError(t, err)
	charge, err := price.Charge(10, 5, 0, 0)
	require.NoError(t, err)
	event := regionalLedgerEvent(request, charge)
	require.NoError(t, holds.Complete(ctx, event))
	require.NoError(t, holds.Complete(ctx, event))
	var platform string
	var actualGroup, actualSubscription int64
	var billingType, count int
	require.NoError(t, integrationDB.QueryRow("SELECT platform,group_id,subscription_id,billing_type FROM usage_logs WHERE request_id=$1", request.RequestID).Scan(&platform, &actualGroup, &actualSubscription, &billingType))
	require.Equal(t, service.PlatformOpenAI, platform, "settlement must preserve the admission platform")
	require.Equal(t, group.ID, actualGroup)
	require.Equal(t, subscription, actualSubscription)
	require.Equal(t, 1, billingType)
	require.NoError(t, integrationDB.QueryRow("SELECT COUNT(*) FROM usage_logs WHERE request_id=$1", request.RequestID).Scan(&count))
	require.Equal(t, 1, count)
	var balance string
	require.NoError(t, integrationDB.QueryRow("SELECT balance::text FROM users WHERE id=$1", user).Scan(&balance))
	require.Equal(t, "10.00000000", balance)
}

func TestRegionalLegacyUnknownAllocationKeepsMoneyAndDrainsBeforeNewAdmission(t *testing.T) {
	ctx := context.Background()
	user := regionalLedgerUser(t)
	client := testEntClient(t)
	group := mustCreateGroup(t, client, &service.Group{Name: "legacy-group-" + uuid.NewString(), Platform: service.PlatformOpenAI, RateMultiplier: 1})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user, GroupID: &group.ID, Key: "sk-mock-" + uuid.NewString(), Name: "legacy"})
	account := mustCreateAccount(t, client, &service.Account{Name: "legacy-" + uuid.NewString(), Type: service.AccountTypeAPIKey, Platform: service.PlatformOpenAI, Status: "active", Schedulable: true})
	_, err := integrationDB.Exec("INSERT INTO account_groups(account_id,group_id)VALUES($1,$2)", account.ID, group.ID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec("DELETE FROM usage_logs WHERE user_id=$1", user)
		_, _ = integrationDB.Exec("DELETE FROM regional_primary_requests WHERE user_id=$1", user)
		_, _ = integrationDB.Exec("DELETE FROM regional_budget_allocations WHERE user_id=$1", user)
	})
	_, holds, _ := NewRegionalFundsAuthorities(integrationDB, func(context.Context, *sql.Tx) error { return nil })
	price := ledger.Price{Model: "mock-legacy-allocation", Input: 1_000_000_000, Output: 2_000_000_000, MaxInput: 100, MaxOutput: 100}
	require.NoError(t, ledger.InstallPrice(ctx, integrationDB, price))
	lease := regionalLedgerLease("not-used", user, 1_000_000_000)
	request := regionalLedgerReservation(lease, "legacy-"+uuid.NewString(), 1_000_000_000)
	request.LeaseID = ""
	request.APIKeyID = key.ID
	request.AccountID = account.ID
	request.Model = price.Model
	request.PricingVersion = price.Version()
	_, err = holds.Reserve(ctx, request)
	require.NoError(t, err)
	require.NoError(t, holds.Start(ctx, request.RequestID))
	// Exact state of an old held balance allocation after additive migration 284.
	_, err = integrationDB.Exec("UPDATE regional_budget_allocations SET group_id=NULL,platform='' WHERE allocation_id=$1", "request:"+request.RequestID)
	require.NoError(t, err)
	next := request
	next.RequestID = "new-" + uuid.NewString()
	_, err = holds.Reserve(ctx, next)
	require.ErrorContains(t, err, "attribution drain")
	var balance string
	require.NoError(t, integrationDB.QueryRow("SELECT balance::text FROM users WHERE id=$1", user).Scan(&balance))
	require.Equal(t, "9.00000000", balance)
	charge, err := price.Charge(10, 5, 0, 0)
	require.NoError(t, err)
	event := regionalLedgerEvent(request, charge)
	require.NoError(t, holds.Complete(ctx, event))
	require.NoError(t, holds.Complete(ctx, event))
	var actualPlatform sql.NullString
	var actualGroup sql.NullInt64
	var billingType int
	require.NoError(t, integrationDB.QueryRow("SELECT platform,group_id,billing_type FROM usage_logs WHERE request_id=$1", request.RequestID).Scan(&actualPlatform, &actualGroup, &billingType))
	require.False(t, actualGroup.Valid)
	require.False(t, actualPlatform.Valid, "an allocation without platform evidence must remain unknown")
	require.Zero(t, billingType)
	fresh, err := holds.Reserve(ctx, next)
	require.NoError(t, err)
	require.True(t, fresh)
	require.NoError(t, holds.CancelBeforeStart(ctx, next.RequestID))
	require.NoError(t, integrationDB.QueryRow("SELECT balance::text FROM users WHERE id=$1", user).Scan(&balance))
	require.Equal(t, "9.99998000", balance)
}
