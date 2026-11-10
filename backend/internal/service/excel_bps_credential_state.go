package service

import (
	"encoding/json"
	"time"
)

const ExcelBPSCredentialStateExtraKey = "openai_excel_bps_credential_state"

// Safe, read-only metadata: no tokens, ciphertext, credential hashes or upstream bodies.
type ExcelBPSCredentialState struct {
	Status               string     `json:"status"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	ObservedAt           *time.Time `json:"observed_at,omitempty"`
	ErrorCode            string     `json:"error_code,omitempty"`
	RequiresManualResume bool       `json:"requires_manual_resume,omitempty"`
}

func ExcelBPSGrantState(expiresAt int64) ExcelBPSCredentialState {
	now := time.Now().UTC()
	state := ExcelBPSCredentialState{Status: "unknown", ObservedAt: &now}
	if expiresAt > 0 {
		expiry := time.Unix(expiresAt, 0).UTC()
		state.ExpiresAt = &expiry
		state.Status = "not_expired"
	}
	return state
}

func ExcelBPSGrantFailure(code string) ExcelBPSCredentialState {
	now := time.Now().UTC()
	state := ExcelBPSCredentialState{Status: "auth_failed", ErrorCode: "authentication_failed", ObservedAt: &now}
	if code == "token_revoked" || code == "token_invalidated" {
		state.Status = "revoked"
		state.ErrorCode = code
	}
	return state
}

func ExcelBPSCredentialStateFromAccount(account *Account, now time.Time) *ExcelBPSCredentialState {
	if account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth || (account.Extra["openai_excel_bps"] != true && !account.IsExcelOAuth()) {
		return nil
	}
	if account.HasManagedOpenAIConnections() {
		return managedExcelBPSCredentialState(account)
	}
	state := ExcelBPSCredentialState{Status: "unknown"}
	if account.IsExcelOAuth() {
		state.ExpiresAt = account.GetCredentialAsTime("expires_at")
		if state.ExpiresAt != nil {
			state.Status = "not_expired"
		}
	} else if raw, ok := account.Extra[ExcelBPSCredentialStateExtraKey]; ok {
		encoded, err := json.Marshal(raw)
		if err == nil {
			_ = json.Unmarshal(encoded, &state)
		}
	}
	// Only emit bounded, server-defined statuses/codes even for legacy imported rows.
	switch state.Status {
	case "not_expired", "expired", "revoked", "auth_failed", "unknown":
	default:
		state.Status = "unknown"
	}
	switch state.ErrorCode {
	case "token_revoked", "token_invalidated", "authentication_failed":
	default:
		state.ErrorCode = ""
	}
	if state.Status != "revoked" && state.ExpiresAt != nil && !now.Before(*state.ExpiresAt) {
		state.Status = "expired"
	}
	if account.Extra[ExcelBPSAuthorizationPendingKey] == true && state.Status != "revoked" && state.Status != "auth_failed" && state.Status != "expired" {
		state.Status = "pending"
	}
	state.RequiresManualResume = !account.Schedulable || account.Status != StatusActive
	return &state
}

// Managed accounts authorize BPS through their authorization connections. They
// never receive a legacy Excel grant, so the legacy diagnosis and the
// preparation marker kept by the grant worker do not describe them. Report only
// a BPS connection that still awaits authorization; an active connection, or a
// preferred non-BPS connection, is shown by the authorization connections panel.
func managedExcelBPSCredentialState(account *Account) *ExcelBPSCredentialState {
	raw, err := json.Marshal(account.Extra["openai_auth_connections"])
	if err != nil {
		return nil
	}
	var connections []*OpenAIAuthConnection
	if json.Unmarshal(raw, &connections) != nil {
		return nil
	}
	var bps *OpenAIAuthConnection
	for _, c := range connections {
		if c == nil || c.Legacy {
			continue
		}
		if c.Preferred {
			bps = c
			break
		}
		if bps == nil && c.Kind == "bps" {
			bps = c
		}
	}
	if bps == nil || bps.Kind != "bps" || bps.Status != "pending" {
		return nil
	}
	return &ExcelBPSCredentialState{Status: "pending", RequiresManualResume: !account.Schedulable || account.Status != StatusActive}
}
