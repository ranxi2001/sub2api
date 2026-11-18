//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func newAPIIntegrationAccounts(t *testing.T) (*service.Account, *service.Account, string) {
	t.Helper()
	site := fmt.Sprintf("https://new-api-%d.example/tenant", time.Now().UnixNano())
	client := testEntClient(t)
	a := mustCreateAccount(t, client, &service.Account{Name: "new-api-first", Platform: "openai", Type: "apikey", Credentials: map[string]any{"api_key": "sk-first", "base_url": site + "/v1"}, Extra: map[string]any{service.AccountCostMultiplierExtraKey: 0.3}})
	b := mustCreateAccount(t, client, &service.Account{Name: "new-api-second", Platform: "openai", Type: "apikey", Credentials: map[string]any{"api_key": "sk-second", "base_url": site}, Extra: map[string]any{service.AccountCostMultiplierExtraKey: 0.7}})
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM new_api_site_authorizations WHERE site_url=$1`, site)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id IN($1,$2)`, a.ID, b.ID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id IN($1,$2)`, a.ID, b.ID)
	})
	return a, b, site
}
func TestNewAPIIntegrationSharedEncryptedRotationAndUnbind(t *testing.T) {
	ctx := context.Background()
	a, b, site := newAPIIntegrationAccounts(t)
	r := NewNewAPIAuthorizationRepository(integrationDB)
	enc, e := NewAESEncryptor(&config.Config{Totp: config.TotpConfig{EncryptionKey: strings.Repeat("42", 32)}})
	require.NoError(t, e)
	cipher, e := enc.Encrypt("secret-first")
	require.NoError(t, e)
	p := &service.NewAPISiteAuthorization{SiteURL: site, UserID: 7, Ciphertext: cipher}
	require.NoError(t, r.Save(ctx, p, []service.NewAPIBindingSave{{Account: a, TokenID: 3, Group: "vip"}, {Account: b, TokenID: 4, Group: "default"}}))
	pa, e := r.GetProfile(ctx, site, 7)
	require.NoError(t, e)
	require.NotEqual(t, "secret-first", pa.Ciphertext)
	secret, e := enc.Decrypt(pa.Ciphertext)
	require.NoError(t, e)
	require.Equal(t, "secret-first", secret)
	ba, e := r.GetBinding(ctx, a.ID)
	require.NoError(t, e)
	bb, e := r.GetBinding(ctx, b.ID)
	require.NoError(t, e)
	require.Equal(t, ba.Profile.ID, bb.Profile.ID)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM new_api_site_authorizations WHERE site_url=$1`, site).Scan(&count))
	require.Equal(t, 1, count)
	ar := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	loaded, e := ar.GetByID(ctx, b.ID)
	require.NoError(t, e)
	now := time.Now().UTC()
	snap := &service.UpstreamBillingProbeSnapshot{Status: "ok", LastAttemptAt: now, NextProbeAt: now.Add(time.Hour), Data: map[string]any{"provider": "new_api", "object": "new_api.group_billing", "billing_scope": "group", "resolved_rate_multiplier": 9.0, "effective_rate_multiplier": 9.0, "peak_rate_enabled": false}}
	require.NoError(t, r.WriteSnapshot(ctx, loaded, bb, snap))
	updated, e := ar.GetByID(ctx, b.ID)
	require.NoError(t, e)
	require.Equal(t, 63.0, updated.CostMultiplier(), "existing New API accounts default to applying the non-CN factor")
	require.Equal(t, 1.0, updated.BillingRateMultiplier())
	require.Equal(t, "new_api", updated.Extra[service.UpstreamBillingProviderExtraKey])
	require.True(t, updated.Extra[service.UpstreamBillingProbeEnabledExtraKey].(bool))
	require.Contains(t, updated.Extra, service.UpstreamBillingProbeExtraKey)
	stale := *bb
	pa.Ciphertext, e = enc.Encrypt("secret-rotated")
	require.NoError(t, e)
	require.NoError(t, r.Save(ctx, pa, []service.NewAPIBindingSave{{Account: a, TokenID: 3, Group: "vip"}}))
	updated, e = ar.GetByID(ctx, b.ID)
	require.NoError(t, e)
	require.NotContains(t, updated.Extra, service.UpstreamBillingProbeExtraKey)
	require.ErrorIs(t, r.WriteSnapshot(ctx, updated, &stale, snap), service.ErrUpstreamBillingProbeIdentityChanged)
	require.NoError(t, r.Unbind(ctx, a.ID))
	ba, e = r.GetBinding(ctx, a.ID)
	require.NoError(t, e)
	require.Nil(t, ba)
	bb, e = r.GetBinding(ctx, b.ID)
	require.NoError(t, e)
	require.NotNil(t, bb)
	require.Equal(t, int64(2), bb.Profile.Revision)
	secret, e = enc.Decrypt(bb.Profile.Ciphertext)
	require.NoError(t, e)
	require.Equal(t, "secret-rotated", secret)
	updated, e = ar.GetByID(ctx, a.ID)
	require.NoError(t, e)
	require.NotContains(t, updated.Extra, service.UpstreamBillingProviderExtraKey)
}
func TestNewAPIIntegrationChangedSecondAccountSavesNothing(t *testing.T) {
	ctx := context.Background()
	a, b, site := newAPIIntegrationAccounts(t)
	r := NewNewAPIAuthorizationRepository(integrationDB)
	_, e := integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(credentials,'{api_key}','"changed"'::jsonb) WHERE id=$1`, b.ID)
	require.NoError(t, e)
	e = r.Save(ctx, &service.NewAPISiteAuthorization{SiteURL: site, UserID: 7, Ciphertext: "encrypted-test-only"}, []service.NewAPIBindingSave{{Account: a, TokenID: 3, Group: "vip"}, {Account: b, TokenID: 4, Group: "default"}})
	require.ErrorIs(t, e, service.ErrUpstreamBillingProbeIdentityChanged)
	p, e := r.GetProfile(ctx, site, 7)
	require.NoError(t, e)
	require.Nil(t, p)
	ba, e := r.GetBinding(ctx, a.ID)
	require.NoError(t, e)
	require.Nil(t, ba)
	var extra []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT extra FROM accounts WHERE id=$1`, a.ID).Scan(&extra))
	var data map[string]any
	require.NoError(t, json.Unmarshal(extra, &data))
	require.NotContains(t, data, service.UpstreamBillingProviderExtraKey)
	require.NotContains(t, data, service.UpstreamBillingProbeEnabledExtraKey)
}
func TestNewAPIIntegrationLegacyProbeCannotOverwriteConfiguredBinding(t *testing.T) {
	ctx := context.Background()
	a, _, site := newAPIIntegrationAccounts(t)
	ar := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	require.NoError(t, ar.UpdateExtra(ctx, a.ID, map[string]any{service.UpstreamBillingProbeEnabledExtraKey: true, service.UpstreamBillingRateSyncEnabledExtraKey: false}))
	legacy, e := ar.GetByID(ctx, a.ID)
	require.NoError(t, e)
	r := NewNewAPIAuthorizationRepository(integrationDB)
	require.NoError(t, r.Save(ctx, &service.NewAPISiteAuthorization{SiteURL: site, UserID: 7, Ciphertext: "test-cipher"}, []service.NewAPIBindingSave{{Account: legacy, TokenID: 3, Group: "vip"}}))
	snap := &service.UpstreamBillingProbeSnapshot{Status: "ok", LastAttemptAt: time.Now(), Data: map[string]any{"billing_scope": "token", "resolved_rate_multiplier": 4.0, "effective_rate_multiplier": 4.0, "peak_rate_enabled": false}}
	require.ErrorIs(t, ar.UpdateUpstreamBillingProbeSnapshot(ctx, legacy, snap, nil), service.ErrUpstreamBillingProbeIdentityChanged)
	current, e := ar.GetByID(ctx, a.ID)
	require.NoError(t, e)
	require.NotContains(t, current.Extra, service.UpstreamBillingProbeExtraKey)
	require.Equal(t, 0.3, current.CostMultiplier())
}
func TestNewAPIIntegrationAccountEditPreservesThenInvalidatesBindingMarker(t *testing.T) {
	ctx := context.Background()
	a, _, site := newAPIIntegrationAccounts(t)
	ar := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	r := NewNewAPIAuthorizationRepository(integrationDB)
	require.NoError(t, r.Save(ctx, &service.NewAPISiteAuthorization{SiteURL: site, UserID: 7, Ciphertext: "test-cipher"}, []service.NewAPIBindingSave{{Account: a, TokenID: 3, Group: "vip"}}))
	current, e := ar.GetByID(ctx, a.ID)
	require.NoError(t, e)
	current.Extra = map[string]any{service.UpstreamBillingProviderExtraKey: "forged"}
	current.Name = "unrelated edit"
	require.NoError(t, ar.Update(ctx, current))
	current, e = ar.GetByID(ctx, a.ID)
	require.NoError(t, e)
	require.Equal(t, "new_api", current.Extra[service.UpstreamBillingProviderExtraKey])
	current.Credentials["api_key"] = "changed"
	require.NoError(t, ar.Update(ctx, current))
	current, e = ar.GetByID(ctx, a.ID)
	require.NoError(t, e)
	require.NotContains(t, current.Extra, service.UpstreamBillingProviderExtraKey)
	binding, e := r.GetBinding(ctx, a.ID)
	require.NoError(t, e)
	require.Nil(t, binding)
}
func TestNewAPIIntegrationGroupRateSortReadsOnlyValidatedGroupSnapshot(t *testing.T) {
	ctx := context.Background()
	a, _, _ := newAPIIntegrationAccounts(t)
	snap := map[string]any{"status": "ok", "data": map[string]any{"provider": "new_api", "object": "new_api.group_billing", "billing_scope": "group", "resolved_rate_multiplier": 0.5, "effective_rate_multiplier": 0.5, "peak_rate_enabled": false}}
	raw, e := json.Marshal(map[string]any{service.UpstreamBillingProbeExtraKey: snap})
	require.NoError(t, e)
	_, e = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra||$1::jsonb WHERE id=$2`, string(raw), a.ID)
	require.NoError(t, e)
	var rate sql.NullFloat64
	e = integrationDB.QueryRowContext(ctx, `SELECT `+upstreamBillingRateSortExpression("extra")+` FROM accounts WHERE id=$1`, a.ID).Scan(&rate)
	require.NoError(t, e)
	require.True(t, rate.Valid)
	require.Equal(t, 0.5, rate.Float64)
}
func TestNewAPIIntegrationDirectOrBulkCredentialsUnbind(t *testing.T) {
	for _, mode := range []string{"direct", "bulk"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			a, _, site := newAPIIntegrationAccounts(t)
			r := NewNewAPIAuthorizationRepository(integrationDB)
			require.NoError(t, r.Save(ctx, &service.NewAPISiteAuthorization{SiteURL: site, UserID: 7, Ciphertext: "test-cipher"}, []service.NewAPIBindingSave{{Account: a, TokenID: 3, Group: "vip"}}))
			ar := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
			if mode == "direct" {
				_, e := integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(credentials,'{api_key}','"new-key"'::jsonb) WHERE id=$1`, a.ID)
				require.NoError(t, e)
			} else {
				_, e := ar.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{Credentials: map[string]any{"api_key": "new-key"}, Extra: map[string]any{service.UpstreamBillingProbeExtraKey: nil}})
				require.NoError(t, e)
			}
			binding, e := r.GetBinding(ctx, a.ID)
			require.NoError(t, e)
			require.Nil(t, binding)
			current, e := ar.GetByID(ctx, a.ID)
			require.NoError(t, e)
			require.NotContains(t, current.Extra, service.UpstreamBillingProviderExtraKey)
			require.NotContains(t, current.Extra, service.UpstreamBillingProbeExtraKey)
		})
	}
}

func TestNewAPIIntegrationConvertedCostAndConcurrentSettings(t *testing.T) {
	ctx := context.Background()
	a, b, site := newAPIIntegrationAccounts(t)
	r := NewNewAPIAuthorizationRepository(integrationDB)
	ar := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET platform='kimi' WHERE id=$1`, b.ID)
	require.NoError(t, err)
	b, err = ar.GetByID(ctx, b.ID)
	require.NoError(t, err)
	enabled, cost := true, 0.7
	require.NoError(t, r.Save(ctx, &service.NewAPISiteAuthorization{SiteURL: site, UserID: 7, Ciphertext: "fixture-encrypted"}, []service.NewAPIBindingSave{{Account: a, TokenID: 3, Group: "foreign", ApplyUSDRate: &enabled, CostMultiplier: &cost}, {Account: b, TokenID: 4, Group: "domestic", ApplyUSDRate: &enabled, CostMultiplier: &cost}}))
	binding, err := r.GetBinding(ctx, a.ID)
	require.NoError(t, err)
	loaded, err := ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 0.7, loaded.CostMultiplier())
	require.Equal(t, 7.0, service.NewAPICostScale(loaded))
	require.True(t, service.NewAPICostSyncEnabled(loaded))
	require.Equal(t, 1.0, loaded.BillingRateMultiplier())
	snap := &service.UpstreamBillingProbeSnapshot{Status: "ok", LastAttemptAt: time.Now(), Data: map[string]any{"provider": "new_api", "object": "new_api.group_billing", "billing_scope": "group", "effective_rate_multiplier": 0.2}}
	require.NoError(t, r.WriteSnapshot(ctx, loaded, binding, snap))
	loaded, err = ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 1.4, loaded.CostMultiplier())
	require.Equal(t, 1.0, loaded.BillingRateMultiplier())
	domestic, err := ar.GetByID(ctx, b.ID)
	require.NoError(t, err)
	dbinding, err := r.GetBinding(ctx, b.ID)
	require.NoError(t, err)
	require.NoError(t, r.WriteSnapshot(ctx, domestic, dbinding, snap))
	domestic, err = ar.GetByID(ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, 0.2, domestic.CostMultiplier())
	// An in-flight ×7 result must not overwrite the new ×1 setting.
	require.NoError(t, ar.UpdateExtra(ctx, a.ID, map[string]any{service.NewAPIApplyUSDRateExtraKey: false, service.AccountCostMultiplierExtraKey: 0.55}))
	require.ErrorIs(t, r.WriteSnapshot(ctx, loaded, binding, snap), service.ErrUpstreamBillingProbeIdentityChanged)
	loaded, err = ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 0.55, loaded.CostMultiplier())
	require.False(t, service.NewAPIApplyUSDRate(loaded))
	require.NoError(t, r.WriteSnapshot(ctx, loaded, binding, snap))
	loaded, err = ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 0.2, loaded.CostMultiplier(), "turning USD conversion off still syncs at x1")
	// The existing independent manual-cost opt-out still wins.
	require.NoError(t, ar.UpdateExtra(ctx, a.ID, map[string]any{service.AccountCostAutoSyncExtraKey: false, service.AccountCostMultiplierExtraKey: 0.55}))
	require.NoError(t, r.WriteSnapshot(ctx, loaded, binding, snap))
	loaded, err = ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 0.55, loaded.CostMultiplier())
	p, err := r.GetProfile(ctx, site, 7)
	require.NoError(t, err)
	enabled = false
	require.NoError(t, r.Save(ctx, p, []service.NewAPIBindingSave{{Account: loaded, TokenID: 3, Group: "foreign", ApplyUSDRate: &enabled}}))
	loaded, err = ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 0.55, loaded.CostMultiplier())
	require.False(t, service.NewAPIApplyUSDRate(loaded))
}
