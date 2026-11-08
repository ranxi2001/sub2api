package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func issue730Credentials(idAuth, accessAuth map[string]any, email string) map[string]any {
	id := map[string]any{"sid": "new-login-session", "sub": "new-subject", "exp": time.Now().Add(time.Hour).Unix()}
	if email != "" {
		id["email"] = email
	}
	if idAuth != nil {
		id["https://api.openai.com/auth"] = idAuth
	}
	access := map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": accessAuth}
	if email != "" {
		access["https://api.openai.com/profile"] = map[string]any{"email": email}
	}
	return map[string]any{"access_token": reauthTestJWT(access), "id_token": reauthTestJWT(id), "refresh_token": "synthetic-refresh"}
}

func TestIssue730ExplicitAccessIdentityPrecedesSessionFallback(t *testing.T) {
	for _, idAuth := range []map[string]any{nil, {"user_id": "user-1"}} {
		svc, reader, repo, updater, _, _ := newReauthTestService("acct-1")
		savePasswordReauthConfig(t, svc)
		ctx := context.Background()
		_, err := svc.CreateTask(ctx, 42)
		require.NoError(t, err)
		claim, err := svc.ClaimTask(ctx, "worker-a")
		require.NoError(t, err)
		credentials := issue730Credentials(idAuth, map[string]any{"chatgpt_account_id": "acct-1", "chatgpt_user_id": "user-1"}, "User@Example.com")
		task, err := svc.SubmitCredentials(ctx, claim.TaskID, "worker-a", credentials, map[string]any{"email": "untrusted@example.test"})
		require.NoError(t, err, "the explicit access-token workspace must override a session ID")
		require.Equal(t, OpenAIOAuthReauthStatusSucceeded, task.Status)
		require.Equal(t, "acct-1", updater.credentials["chatgpt_account_id"])
		require.Equal(t, "user-1", updater.credentials["chatgpt_user_id"])
		require.Equal(t, reader.account.Credentials["custom_setting"], updater.credentials["custom_setting"])
		require.Equal(t, 0, repo.markSucceeded, "credential CAS finalizes the task atomically")
	}
}

func TestIssue730IdentityDiagnosticsArePreciseAndPrivate(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		mutate       func(map[string]any)
	}{
		{"account missing", "account_id_missing", func(c map[string]any) {
			c["id_token"] = reauthTestJWT(map[string]any{"email": "user@example.com", "https://api.openai.com/auth": map[string]any{"user_id": "user-1"}})
			c["access_token"] = "opaque-access"
		}},
		{"user missing", "user_id_missing", func(c map[string]any) {
			c["id_token"] = reauthTestJWT(map[string]any{"sid": "acct-1", "email": "user@example.com"})
			c["access_token"] = "opaque-access"
		}},
		{"email missing", "email_missing", func(c map[string]any) {
			c["id_token"] = reauthTestJWT(map[string]any{"sid": "acct-1", "sub": "user-1"})
			c["access_token"] = "opaque-access"
		}},
		{"workspace mismatch", "account_id_mismatch", func(c map[string]any) {
			c["id_token"] = reauthTestJWT(map[string]any{"sid": "other-account", "email": "user@example.com", "https://api.openai.com/auth": map[string]any{"user_id": "user-1"}})
			c["access_token"] = "opaque-access"
		}},
		{"user mismatch", "user_id_mismatch", func(c map[string]any) {
			c["id_token"] = reauthTestJWT(map[string]any{"sid": "acct-1", "email": "user@example.com", "https://api.openai.com/auth": map[string]any{"user_id": "other-user"}})
			c["access_token"] = "opaque-access"
		}},
		{"email mismatch", "email_mismatch", func(c map[string]any) {
			c["id_token"] = reauthTestJWT(map[string]any{"sid": "acct-1", "email": "other@example.test", "https://api.openai.com/auth": map[string]any{"user_id": "user-1"}})
			c["access_token"] = "opaque-access"
		}},
		{"account token conflict", "token_account_conflict", func(c map[string]any) {
			c["access_token"] = reauthTestJWT(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "other-account", "chatgpt_user_id": "user-1"}})
		}},
		{"user token conflict", "token_user_conflict", func(c map[string]any) {
			c["access_token"] = reauthTestJWT(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1", "chatgpt_user_id": "other-user"}})
		}},
		{"email token conflict", "token_email_conflict", func(c map[string]any) {
			c["access_token"] = reauthTestJWT(map[string]any{"https://api.openai.com/profile": map[string]any{"email": "other@example.test"}})
		}},
		{"invalid ID", "id_token_invalid", func(c map[string]any) { c["id_token"] = "malformed.private.token" }},
		{"invalid access", "access_token_invalid", func(c map[string]any) { c["access_token"] = "malformed.private.token" }},
		{"partial credentials", "credentials_incomplete", func(c map[string]any) { delete(c, "refresh_token") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, repo, updater, _, blocker := newReauthTestService("acct-1")
			savePasswordReauthConfig(t, svc)
			ctx := context.Background()
			_, err := svc.CreateTask(ctx, 42)
			require.NoError(t, err)
			claim, err := svc.ClaimTask(ctx, "worker-a")
			require.NoError(t, err)
			credentials := issue730Credentials(map[string]any{"chatgpt_account_id": "acct-1", "user_id": "user-1"}, map[string]any{"chatgpt_account_id": "acct-1", "user_id": "user-1"}, "user@example.com")
			tc.mutate(credentials)
			_, err = svc.SubmitCredentials(ctx, claim.TaskID, "worker-a", credentials, map[string]any{"email": "user@example.com", "chatgpt_account_id": "acct-1", "chatgpt_user_id": "user-1"})
			require.Error(t, err)
			require.False(t, updater.applied)
			require.Zero(t, blocker.clearedAccountID)
			require.Equal(t, OpenAIOAuthReauthStatusFailed, repo.task.Status)
			projected := safeReauthTask(repo.task)
			require.NotNil(t, projected.Diagnostic)
			require.Equal(t, "credentials_validation", projected.Diagnostic.Stage)
			require.Equal(t, tc.reason, projected.Diagnostic.Reason)
			require.NotNil(t, projected.Diagnostic.Identity)
			encoded, err := json.Marshal(projected)
			require.NoError(t, err)
			for _, secret := range []string{"user@example.com", "other@example.test", "acct-1", "user-1", "other-account", "other-user", "synthetic-refresh", "private", "password-secret", "totp-secret"} {
				require.NotContains(t, string(encoded), secret)
			}
			require.Equal(t, "credentials_validation", projected.Failure.Stage)
			require.Error(t, svc.FailTask(ctx, claim.TaskID, "wrong-worker", "arbitrary-private"))
			require.Equal(t, tc.reason, safeReauthTask(repo.task).Diagnostic.Reason)
		})
	}
}

func TestIssue730ManualAndAutomaticTasksShareIdentityAndBudgetSemantics(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprint(automatic), func(t *testing.T) {
			svc, _, repo, updater, _, _ := newReauthTestService("acct-1")
			savePasswordReauthConfig(t, svc)
			ctx := context.Background()
			var creator func(context.Context, int64, string) (*OpenAIOAuthReauthTaskRecord, error)
			created := false
			if automatic {
				creator = func(ctx context.Context, accountID int64, hash string) (*OpenAIOAuthReauthTaskRecord, error) {
					created = true
					return repo.CreateTask(ctx, accountID, hash)
				}
			}
			_, err := svc.createTaskForProfile(ctx, 42, "codex", creator)
			require.NoError(t, err)
			require.Equal(t, automatic, created)
			claim, err := svc.ClaimTask(ctx, "worker-a")
			require.NoError(t, err)
			require.Equal(t, "acct-1", claim.ExpectedWorkspace)
			_, err = svc.SubmitCredentials(ctx, claim.TaskID, "worker-a", directReauthCredentials("wrong-workspace", "user-1", "user@example.com"), nil)
			require.Equal(t, http.StatusConflict, infraerrors.Code(err))
			require.False(t, updater.applied)
			require.Equal(t, "account_id_mismatch", safeReauthTask(repo.task).Diagnostic.Reason)
			finished := time.Now()
			outcome := AccountReloginOutcome{ID: repo.task.ID, Status: repo.task.Status, FinishedAt: &finished}
			rules := budgetTestRules()
			updated, pending := AdvanceAccountReloginBudget(AccountTokenGuardV2Record{Enabled: true, AutoReloginEnabled: true}, []AccountReloginOutcome{outcome}, rules, finished, AccountTokenGuardV2ProbeAuth)
			require.False(t, pending)
			require.Equal(t, 1, updated.ReloginFailStreak)
			again, _ := AdvanceAccountReloginBudget(updated, []AccountReloginOutcome{outcome}, rules, finished.Add(time.Second), AccountTokenGuardV2ProbeAuth)
			require.Equal(t, updated, again, "an identity failure consumes its existing budget exactly once")
		})
	}
}

func TestIssue730CallbackUsesSameTokenIdentityResolution(t *testing.T) {
	svc, _, _, updater, client, _ := newReauthTestService("unused")
	client.accountID = "acct-1"
	ctx := context.Background()
	_, err := svc.SaveConfig(ctx, 42, "user@example.com", "https://mail.example.test/code")
	require.NoError(t, err)
	_, err = svc.CreateTask(ctx, 42)
	require.NoError(t, err)
	claim, err := svc.ClaimTask(ctx, "worker-a")
	require.NoError(t, err)
	_, err = svc.SubmitCallback(ctx, claim.TaskID, "worker-a", "http://localhost:1455/auth/callback?code=synthetic&state="+url.QueryEscape(reauthStateFromURL(t, claim.AuthURL)))
	require.NoError(t, err)
	require.True(t, updater.applied)
}

func TestIssue730DiagnosticRejectsInjectedOrAmbiguousText(t *testing.T) {
	checks := reauthIdentityChecks(&Account{Credentials: map[string]any{"chatgpt_account_id": "secret-workspace"}}, &OpenAITokenInfo{ChatGPTAccountID: "other-workspace"})
	valid := reauthIdentityError("account_id_mismatch", checks)
	_, diagnostic, handled := parseReauthIdentityError(valid)
	require.True(t, handled)
	require.NotNil(t, diagnostic)
	require.True(t, diagnostic.Identity.Account.Present)
	require.False(t, diagnostic.Identity.Account.Matches)
	for _, raw := range []string{valid + " secret", valid + "\nsecret", strings.Replace(valid, "account_id_mismatch", "secret", 1), strings.Replace(valid, "account=110", "account=001", 1), strings.Replace(valid, "reason=", "", 1)} {
		safe, projected, handled := parseReauthIdentityError(raw)
		require.True(t, handled)
		require.Nil(t, projected)
		require.NotContains(t, safe, "secret")
	}
	for _, raw := range []string{
		`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1","chatgpt_account_id":"other"}}`,
		`{"https://api.openai.com/auth":{"user_id":"user-1","user\u005fid":"other"}}`,
	} {
		token := "header." + base64.RawURLEncoding.EncodeToString([]byte(raw)) + ".signature"
		info := &OpenAITokenInfo{IDToken: token, AccessToken: "opaque-access", ChatGPTAccountID: "acct-1"}
		require.Equal(t, "id_token_invalid", resolveReauthIdentity(info))
	}
}

func TestIssue730AccessProfileCanSupplyMissingEmailButMetadataCannot(t *testing.T) {
	svc, _, _, updater, _, _ := newReauthTestService("acct-1")
	savePasswordReauthConfig(t, svc)
	ctx := context.Background()
	_, err := svc.CreateTask(ctx, 42)
	require.NoError(t, err)
	claim, err := svc.ClaimTask(ctx, "worker-a")
	require.NoError(t, err)
	credentials := issue730Credentials(map[string]any{"chatgpt_account_id": "acct-1", "user_id": "user-1"}, map[string]any{"chatgpt_account_id": "acct-1", "user_id": "user-1"}, "")
	credentials["access_token"] = reauthTestJWT(map[string]any{
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": "acct-1", "user_id": "user-1"},
		"https://api.openai.com/profile": map[string]any{"email": "USER@EXAMPLE.COM"},
	})
	_, err = svc.SubmitCredentials(ctx, claim.TaskID, "worker-a", credentials, map[string]any{"email": "untrusted@example.test"})
	require.NoError(t, err)
	require.Equal(t, "USER@EXAMPLE.COM", updater.credentials["email"])
}
