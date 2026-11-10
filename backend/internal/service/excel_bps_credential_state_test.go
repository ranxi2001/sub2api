package service

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestExcelBPSCredentialState(t *testing.T) {
	now := time.Now()
	account := excelAccount()
	account.Credentials["client_id"] = "independent-grant"
	require.Equal(t, "unknown", ExcelBPSCredentialStateFromAccount(account, now).Status)
	account.Extra[ExcelBPSAuthorizationPendingKey] = true
	require.Equal(t, "pending", ExcelBPSCredentialStateFromAccount(account, now).Status)
	delete(account.Extra, ExcelBPSAuthorizationPendingKey)
	state := ExcelBPSGrantState(now.Add(time.Hour).Unix())
	account.Extra[ExcelBPSCredentialStateExtraKey] = state
	require.Equal(t, "not_expired", ExcelBPSCredentialStateFromAccount(account, now).Status)
	require.Equal(t, "expired", ExcelBPSCredentialStateFromAccount(account, now.Add(2*time.Hour)).Status)
	account.Extra[ExcelBPSCredentialStateExtraKey] = ExcelBPSGrantFailure("token_revoked")
	account.Schedulable = false
	got := ExcelBPSCredentialStateFromAccount(account, now)
	require.Equal(t, "revoked", got.Status)
	require.True(t, got.RequiresManualResume)
	require.Equal(t, "authentication_failed", ExcelBPSGrantFailure("PRIVATE_UPSTREAM_TOKEN").ErrorCode)
}

func TestExcelBPSCredentialDiagnosisIsServerOwned(t *testing.T) {
	key := ExcelBPSCredentialStateExtraKey
	require.NotContains(t, MergeOpenAICodexTicketExtra(map[string]any{key: "forged"}, nil), key)
	result := MergeOpenAICodexTicketExtra(map[string]any{key: "stale", "unrelated": true}, map[string]any{key: "current"})
	require.Equal(t, "current", result[key])
	require.Equal(t, true, result["unrelated"])
}

func TestManagedExcelBPSCredentialStateFollowsBPSConnection(t *testing.T) {
	now := time.Now()
	connection := func(kind, status string, preferred bool) map[string]any {
		return map[string]any{"id": kind + "-" + status, "kind": kind, "status": status, "preferred": preferred,
			"capabilities": connectionCapabilities(kind, ""), "refresh_state": "idle", "cooldown_until": "2026-11-11T01:25:39.087714+08:00"}
	}
	managed := func(connections ...map[string]any) *Account {
		account := excelAccount()
		account.Extra["openai_connections_managed"] = true
		// The grant worker keeps this marker because managed accounts never get a legacy Excel grant.
		account.Extra[ExcelBPSAuthorizationPendingKey] = true
		mirror := make([]any, 0, len(connections))
		for _, c := range connections {
			mirror = append(mirror, c)
		}
		account.Extra["openai_auth_connections"] = mirror
		return account
	}
	legacy := connection("bps", "pending", false)
	legacy["legacy"] = true
	for _, tt := range []struct {
		name    string
		account *Account
		want    string
	}{
		{"preferred BPS connection active", managed(connection("bps", "active", true)), ""},
		{"preferred BPS connection awaiting authorization", managed(connection("bps", "pending", true)), "pending"},
		{"preferred Codex connection ignores another pending BPS", managed(connection("bps", "pending", false), connection("codex", "active", true)), ""},
		{"only BPS connection without preference awaiting authorization", managed(connection("bps", "pending", false)), "pending"},
		{"only BPS connection without preference active", managed(connection("bps", "active", false)), ""},
		{"preferred BPS connection disabled", managed(connection("bps", "disabled", true)), ""},
		{"legacy connection record ignored", managed(legacy), ""},
		{"no connection mirror", func() *Account { a := managed(); delete(a.Extra, "openai_auth_connections"); return a }(), ""},
		{"invalid connection mirror", func() *Account { a := managed(); a.Extra["openai_auth_connections"] = "invalid"; return a }(), ""},
		{"legacy grant diagnosis ignored", func() *Account {
			a := managed(connection("bps", "active", true))
			a.Extra[ExcelBPSCredentialStateExtraKey] = ExcelBPSGrantFailure("token_revoked")
			return a
		}(), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := ExcelBPSCredentialStateFromAccount(tt.account, now)
			if tt.want == "" {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tt.want, got.Status)
			require.Nil(t, got.ExpiresAt)
			require.Empty(t, got.ErrorCode, "the legacy grant diagnosis must not leak into a managed account")
			require.False(t, got.RequiresManualResume)
		})
	}

	paused := managed(connection("bps", "pending", true))
	paused.Schedulable = false
	require.True(t, ExcelBPSCredentialStateFromAccount(paused, now).RequiresManualResume)

	unmanaged := managed(connection("bps", "active", true))
	delete(unmanaged.Extra, "openai_connections_managed")
	require.Equal(t, "pending", ExcelBPSCredentialStateFromAccount(unmanaged, now).Status, "legacy accounts keep the preparation marker")
	unmanaged.Extra[ExcelBPSCredentialStateExtraKey] = ExcelBPSGrantFailure("token_revoked")
	require.Equal(t, "revoked", ExcelBPSCredentialStateFromAccount(unmanaged, now).Status, "legacy accounts keep their grant diagnosis")
}
