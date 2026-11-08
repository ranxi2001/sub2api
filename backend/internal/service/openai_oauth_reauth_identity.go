package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Only the trusted OAuth response is a source of candidate identity. Stored
// credentials and worker-supplied metadata must never fill missing claims.
type reauthTokenIdentity struct{ account, user, email, sid, sub string }

func reauthIdentityClaims(token string) (reauthTokenIdentity, bool) {
	var identity reauthTokenIdentity
	if len(token) > 65536 {
		return identity, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return identity, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return identity, false
	}
	claims, ok := credentialOperationObject(payload)
	if !ok {
		return identity, false
	}
	value := func(fields map[string]json.RawMessage, key string) string {
		var text string
		_ = json.Unmarshal(fields[key], &text)
		return strings.TrimSpace(text)
	}
	identity.email = value(claims, "email")
	identity.sid = value(claims, "sid")
	identity.sub = value(claims, "sub")
	if raw, exists := claims["https://api.openai.com/auth"]; exists {
		auth, valid := credentialOperationObject(raw)
		if !valid {
			return identity, false
		}
		identity.account = value(auth, "chatgpt_account_id")
		identity.user = firstNonEmptyReauth(value(auth, "chatgpt_user_id"), value(auth, "user_id"))
	}
	if raw, exists := claims["https://api.openai.com/profile"]; exists {
		profile, valid := credentialOperationObject(raw)
		if !valid {
			return identity, false
		}
		email := value(profile, "email")
		if identity.email != "" && email != "" && !strings.EqualFold(identity.email, email) {
			return identity, false
		}
		identity.email = firstNonEmptyReauth(identity.email, email)
	}
	return identity, true
}

// Explicit account/user claims in either token take precedence over legacy
// sid/sub fallbacks. A sid can describe a new login session, not its workspace.
// This is claim decoding under the existing trusted-worker/token-exchange
// boundary, not JWT signature verification or proof of successful upstream auth.
func resolveReauthIdentity(info *OpenAITokenInfo) string {
	if info == nil {
		return "candidate_missing"
	}
	id, ok := reauthIdentityClaims(info.IDToken)
	if !ok {
		return "id_token_invalid"
	}
	var access reauthTokenIdentity
	if strings.Contains(info.AccessToken, ".") {
		access, ok = reauthIdentityClaims(info.AccessToken)
		if !ok {
			return "access_token_invalid"
		}
	}
	for _, pair := range []struct{ left, right, reason string }{
		{id.account, access.account, "token_account_conflict"},
		{id.user, access.user, "token_user_conflict"},
		{strings.ToLower(id.email), strings.ToLower(access.email), "token_email_conflict"},
	} {
		if pair.left != "" && pair.right != "" && pair.left != pair.right {
			return pair.reason
		}
	}
	info.ChatGPTAccountID = firstNonEmptyReauth(id.account, access.account, id.sid)
	info.ChatGPTUserID = firstNonEmptyReauth(id.user, access.user, id.sub)
	info.Email = firstNonEmptyReauth(id.email, access.email)
	return ""
}

type ReauthIdentityFieldCheck struct {
	StoredPresent bool `json:"stored_present"`
	Present       bool `json:"present"`
	Matches       bool `json:"matches"`
}

type ReauthIdentityChecks struct {
	Account ReauthIdentityFieldCheck `json:"account"`
	User    ReauthIdentityFieldCheck `json:"user"`
	Email   ReauthIdentityFieldCheck `json:"email"`
}

func reauthIdentityChecks(account *Account, info *OpenAITokenInfo) *ReauthIdentityChecks {
	check := func(old, candidate string) ReauthIdentityFieldCheck {
		old, candidate = strings.TrimSpace(old), strings.TrimSpace(candidate)
		return ReauthIdentityFieldCheck{StoredPresent: old != "", Present: candidate != "", Matches: old != "" && candidate != "" && old == candidate}
	}
	if account == nil {
		account = &Account{}
	}
	if info == nil {
		info = &OpenAITokenInfo{}
	}
	return &ReauthIdentityChecks{
		Account: check(account.GetCredential("chatgpt_account_id"), info.ChatGPTAccountID),
		User:    check(account.GetCredential("chatgpt_user_id"), info.ChatGPTUserID),
		Email:   check(strings.ToLower(account.GetCredential("email")), strings.ToLower(info.Email)),
	}
}

func reauthIdentityReason(checks *ReauthIdentityChecks) string {
	for _, field := range []struct {
		name  string
		check ReauthIdentityFieldCheck
	}{{"account_id", checks.Account}, {"user_id", checks.User}, {"email", checks.Email}} {
		if field.check.StoredPresent {
			if !field.check.Present {
				return field.name + "_missing"
			}
			if !field.check.Matches {
				return field.name + "_mismatch"
			}
		}
	}
	return "stored_identity_missing"
}

// The persisted representation contains only fixed enums and nine booleans.
// Parsing reconstructs it exactly, so legacy/untrusted error text cannot smuggle
// identity IDs, emails, credentials or provider payloads through task projection.
func reauthIdentityError(reason string, checks *ReauthIdentityChecks) string {
	bits := func(check ReauthIdentityFieldCheck) string {
		bit := func(value bool) string {
			if value {
				return "1"
			}
			return "0"
		}
		return bit(check.StoredPresent) + bit(check.Present) + bit(check.Matches)
	}
	return fmt.Sprintf("reauth_identity_failed reason=%s account=%s user=%s email=%s", reason, bits(checks.Account), bits(checks.User), bits(checks.Email))
}

func parseReauthIdentityError(raw string) (string, *ReauthDiagnostic, bool) {
	if !strings.HasPrefix(raw, "reauth_identity_failed") {
		return "", nil, false
	}
	invalid := "re-login identity validation failed; invalid diagnostic omitted"
	parts := strings.Split(raw, " ")
	if len(raw) > 180 || len(parts) != 5 || parts[0] != "reauth_identity_failed" {
		return invalid, nil, true
	}
	reason := strings.TrimPrefix(parts[1], "reason=")
	switch reason {
	case "candidate_missing", "stored_identity_missing", "credentials_incomplete", "id_token_invalid", "access_token_invalid",
		"token_account_conflict", "token_user_conflict", "token_email_conflict", "account_id_missing", "account_id_mismatch",
		"user_id_missing", "user_id_mismatch", "email_missing", "email_mismatch":
	default:
		return invalid, nil, true
	}
	checks := &ReauthIdentityChecks{}
	for i, target := range []*ReauthIdentityFieldCheck{&checks.Account, &checks.User, &checks.Email} {
		prefix := []string{"account=", "user=", "email="}[i]
		bits, ok := strings.CutPrefix(parts[i+2], prefix)
		if !ok || len(bits) != 3 || strings.Trim(bits, "01") != "" {
			return invalid, nil, true
		}
		*target = ReauthIdentityFieldCheck{StoredPresent: bits[0] == '1', Present: bits[1] == '1', Matches: bits[2] == '1'}
		if target.Matches && (!target.StoredPresent || !target.Present) {
			return invalid, nil, true
		}
	}
	if reauthIdentityError(reason, checks) != raw {
		return invalid, nil, true
	}
	return raw, &ReauthDiagnostic{Stage: "credentials_validation", Reason: reason, Identity: checks}, true
}
