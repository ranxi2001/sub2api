//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBPSWarmProjectionPreservesEligibilityWithoutLargePayloads(t *testing.T) {
	ctx := context.Background()
	large := strings.Repeat("synthetic-unused-payload", 8192)
	tests := []struct {
		name   string
		change func(*service.Account)
		omit   bool
	}{
		{name: "managed"},
		{name: "static", change: func(a *service.Account) { a.Extra[service.ExcelBPSProxySourceKey] = service.ExcelBPSProxySourceIPPool }},
		{name: "request group", change: func(a *service.Account) {
			a.Extra[service.ExcelBPSProxyModeKey] = service.ExcelBPSProxyModeRequest
			a.Extra[service.ExcelBPSProxyIDsKey] = []int64{11, 22}
		}},
		{name: "free", change: func(a *service.Account) { a.Credentials["plan_type"] = " Free " }},
		{name: "agent", change: func(a *service.Account) { a.Credentials["auth_mode"] = service.OpenAIAuthModeAgentIdentity }},
		{name: "legacy_pat", change: func(a *service.Account) { a.Credentials["openai_auth_mode"] = "personal_access_token" }},
		{name: "siwc", change: func(a *service.Account) { a.Extra[service.OpenAISIWCEnabledKey] = true }},
		{name: "authorization_pending", change: func(a *service.Account) { a.Extra[service.ExcelBPSAuthorizationPendingKey] = true }},
		{name: "paused", change: func(a *service.Account) { a.Schedulable = false }},
		{name: "proxy_disabled", change: func(a *service.Account) { a.Extra["openai_excel_bps_mihomo"] = false }},
		{name: "recovery", change: func(a *service.Account) {
			a.Extra["openai_excel_bps"] = false
			a.Extra[service.ExcelBPSAutoRecoverOn403Key] = true
			a.Extra["openai_excel_bps_auto_disable_on_403"] = true
			a.Extra[service.ExcelBPS403DisabledAtKey] = time.Now().UTC().Format(time.RFC3339Nano)
		}},
		{name: "shadow"},
		{name: "inactive", change: func(a *service.Account) { a.Status = "disabled" }, omit: true},
		{name: "other_platform", change: func(a *service.Account) { a.Platform = service.PlatformAnthropic }, omit: true},
		{name: "deleted", omit: true},
	}
	ids := make([]int64, 0, len(tests))
	t.Cleanup(func() {
		for i := len(ids) - 1; i >= 0; i-- {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, ids[i])
		}
	})
	want := make(map[int64]service.Account)
	omit := make(map[int64]bool)
	for _, tt := range tests {
		a := service.Account{
			Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Status: service.StatusActive, Schedulable: true, Concurrency: 7,
			QuotaDimension: service.QuotaDimensionGlobal,
			Credentials:    map[string]any{"plan_type": "plus", "irrelevant_large_field": large},
			Extra:          map[string]any{"openai_excel_bps": true, "openai_excel_bps_mihomo": true, "irrelevant_large_field": large},
		}
		if tt.change != nil {
			tt.change(&a)
		}
		if tt.name == "shadow" {
			a.ParentAccountID = &ids[0]
			a.QuotaDimension = service.QuotaDimensionSpark
		}
		credentials, err := json.Marshal(a.Credentials)
		require.NoError(t, err)
		extra, err := json.Marshal(a.Extra)
		require.NoError(t, err)
		var storedDimension string
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			INSERT INTO accounts(name,platform,type,status,schedulable,concurrency,credentials,extra,parent_account_id,quota_dimension)
			VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$10) RETURNING id,quota_dimension`,
			"warm-projection-"+tt.name, a.Platform, a.Type, a.Status, a.Schedulable, a.Concurrency,
			string(credentials), string(extra), a.ParentAccountID, a.QuotaDimension).Scan(&a.ID, &storedDimension))
		ids = append(ids, a.ID)
		require.Equal(t, a.QuotaDimension, storedDimension)
		if tt.name == "deleted" {
			_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, a.ID)
			require.NoError(t, err)
		}
		if tt.omit {
			omit[a.ID] = true
		} else {
			want[a.ID] = a
		}
	}
	accounts, err := (&accountRepository{sql: integrationDB}).ListBPSWarmAccounts(ctx)
	require.NoError(t, err)
	seen := make(map[int64]bool)
	for _, got := range accounts {
		require.False(t, omit[got.ID])
		full, ok := want[got.ID]
		if !ok {
			continue
		}
		seen[got.ID] = true
		require.Equal(t, full.IsExcelBPSEnabled(), got.IsExcelBPSEnabled())
		require.Equal(t, full.IsExcelBPS403RecoveryPending(), got.IsExcelBPS403RecoveryPending())
		require.Equal(t, full.IsExcelBPSMihomoEnabled(), got.IsExcelBPSMihomoEnabled())
		require.Equal(t, full.ExcelBPSProxySource(), got.ExcelBPSProxySource())
		require.Equal(t, full.ExcelBPSRequestProxyEnabled(), got.ExcelBPSRequestProxyEnabled())
		require.Equal(t, full.ExcelBPSRequestProxyIDs(), got.ExcelBPSRequestProxyIDs())
		require.Equal(t, full.IsShadow(), got.IsShadow())
		if full.IsShadow() {
			require.True(t, got.IsShadow())
			require.False(t, got.IsExcelBPSEnabled())
			require.False(t, got.IsExcelBPS403RecoveryPending())
		}
		require.Equal(t, full.Schedulable, got.Schedulable)
		require.Equal(t, full.Concurrency, got.Concurrency)
		require.NotContains(t, got.Credentials, "irrelevant_large_field")
		require.NotContains(t, got.Extra, "irrelevant_large_field")
		require.Empty(t, got.Groups)
		require.Nil(t, got.Proxy)
		encoded, err := json.Marshal(got)
		require.NoError(t, err)
		require.Less(t, len(encoded), 4096)
	}
	require.Len(t, seen, len(want))
}
