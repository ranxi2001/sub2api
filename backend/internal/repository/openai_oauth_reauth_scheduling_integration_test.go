//go:build integration

package repository

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIReauthPreservesSchedulingIntent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		extra  map[string]any
		before bool
		want   bool
	}{
		{name: "legacy paused account"},
		{name: "legacy active account", before: true, want: true},
		{name: "opted out paused account", extra: map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: false}},
		{name: "429 paused account", extra: map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: true}},
		{name: "opted in active account", extra: map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: true}, before: true, want: true},
		{name: "string flag does not reopen paused account", extra: map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: "true"}},
		{name: "admission still preserved", extra: map[string]any{service.OpenAITwoFAAdmissionExtraKey: map[string]any{"state": "pending"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
			old := map[string]any{"access_token": "old", "refresh_token": "old-rt"}
			a := mustCreateAccount(t, tx.Client(), &service.Account{
				Name: "reauth-scheduling", Platform: service.PlatformOpenAI,
				Type: service.AccountTypeOAuth, Credentials: old, Extra: tc.extra,
			})
			require.NoError(t, tx.Client().Account.UpdateOneID(a.ID).SetSchedulable(tc.before).Exec(ctx))
			taskID := mustCreateOpenAIReauthCallbackTask(t, tx, a.ID)

			applied, err := repo.ApplyOpenAIOAuthReauth(ctx, taskID, "worker", a.ID, old,
				map[string]any{"access_token": "new", "refresh_token": "new-rt"}, nil)
			require.NoError(t, err)
			require.True(t, applied)
			current, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, tc.want, current.Schedulable)
			require.Equal(t, "new", current.Credentials["access_token"], "preserving the scheduling switch must not block credential replacement")
		})
	}
}

func TestOpenAIReauthAfterAuthErrorPreservesSchedulingIntent(t *testing.T) {
	boolPointer := func(value bool) *bool { return &value }
	for _, tc := range []struct {
		name           string
		extra          map[string]any
		before         bool
		repeated       bool
		pauseBefore401 bool
		afterError     *bool
		want           bool
	}{
		{name: "401 with 429 policy enabled", extra: map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: true}, before: true, want: true},
		{name: "repeated 401 with 429 policy enabled", extra: map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: true}, before: true, repeated: true, want: true},
		{name: "401 without 429 policy", before: true, want: true},
		{name: "429 pause before 401", extra: map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: true}, before: true, pauseBefore401: true},
		{name: "manual pause before 401"},
		{name: "admission pause before 401", extra: map[string]any{service.OpenAITwoFAAdmissionExtraKey: map[string]any{"state": "pending"}}},
		{name: "manual pause while reauth is running", before: true, afterError: boolPointer(false)},
		{name: "manual resume while reauth is running", afterError: boolPointer(true), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
			old := map[string]any{"access_token": "old", "refresh_token": "old-rt"}
			a := mustCreateAccount(t, tx.Client(), &service.Account{
				Name: "reauth-after-401", Platform: service.PlatformOpenAI,
				Type: service.AccountTypeOAuth, Credentials: old, Extra: tc.extra,
			})
			require.NoError(t, repo.SetSchedulable(ctx, a.ID, tc.before))
			if tc.pauseBefore401 {
				// The 429 handler persists its independent pause through this method.
				require.NoError(t, repo.SetSchedulable(ctx, a.ID, false))
			}
			require.NoError(t, repo.SetError(ctx, a.ID, "401 authentication failed"))
			if tc.repeated {
				require.NoError(t, repo.SetError(ctx, a.ID, "401 authentication failed again"))
			}
			failed, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, service.StatusError, failed.Status)
			require.Equal(t, tc.before && !tc.pauseBefore401, failed.Schedulable)
			require.False(t, failed.IsSchedulable(), "authentication errors must block requests even when the scheduling switch remains enabled")
			candidates, err := repo.ListSchedulableByPlatform(ctx, service.PlatformOpenAI)
			require.NoError(t, err)
			for _, candidate := range candidates {
				require.NotEqual(t, a.ID, candidate.ID, "the database scheduler must exclude the failed account")
			}

			taskID := mustCreateOpenAIReauthCallbackTask(t, tx, a.ID)
			if tc.afterError != nil {
				require.NoError(t, repo.SetSchedulable(ctx, a.ID, *tc.afterError))
			}
			applied, err := repo.ApplyOpenAIOAuthReauth(ctx, taskID, "worker", a.ID, old,
				map[string]any{"access_token": "new", "refresh_token": "new-rt"}, nil)
			require.NoError(t, err)
			require.True(t, applied)
			current, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, service.StatusActive, current.Status)
			require.Empty(t, current.ErrorMessage)
			require.Equal(t, tc.want, current.Schedulable)
			require.Equal(t, tc.want, current.IsSchedulable())
			require.Equal(t, "new", current.Credentials["access_token"])
			var taskStatus string
			require.NoError(t, scanSingleRow(ctx, tx, "SELECT status FROM openai_oauth_reauth_tasks WHERE id=$1", []any{taskID}, &taskStatus))
			require.Equal(t, service.OpenAIOAuthReauthStatusSucceeded, taskStatus)
		})
	}
}

func TestOpenAIReauthAuthErrorCredentialCASMissKeepsAccountBlocked(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	old := map[string]any{"access_token": "current", "refresh_token": "current-rt"}
	a := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "reauth-stale-callback", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: old,
		Extra: map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: true},
	})
	require.NoError(t, repo.SetError(ctx, a.ID, "401 authentication failed"))
	taskID := mustCreateOpenAIReauthCallbackTask(t, tx, a.ID)
	applied, err := repo.ApplyOpenAIOAuthReauth(ctx, taskID, "worker", a.ID,
		map[string]any{"access_token": "stale", "refresh_token": "stale-rt"},
		map[string]any{"access_token": "new", "refresh_token": "new-rt"}, nil)
	require.NoError(t, err)
	require.False(t, applied)
	current, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusError, current.Status)
	require.True(t, current.Schedulable, "a rejected callback must not modify scheduling intent")
	require.False(t, current.IsSchedulable())
	require.Equal(t, "current", current.Credentials["access_token"])
	var taskStatus string
	require.NoError(t, scanSingleRow(ctx, tx, "SELECT status FROM openai_oauth_reauth_tasks WHERE id=$1", []any{taskID}, &taskStatus))
	require.Equal(t, service.OpenAIOAuthReauthStatusCallbackProcessing, taskStatus)
}

func mustCreateOpenAIReauthCallbackTask(t *testing.T, tx *dbent.Tx, accountID int64) int64 {
	t.Helper()
	rows, err := tx.Client().QueryContext(context.Background(), `INSERT INTO openai_oauth_reauth_tasks
 (account_id,status,stage,expected_credentials_hash,worker_id)
 VALUES ($1,$2,$3,'synthetic-hash','worker') RETURNING id`,
		accountID, service.OpenAIOAuthReauthStatusCallbackProcessing, service.OpenAIOAuthReauthStageApplyingCredentials)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rows.Close() })
	require.True(t, rows.Next(), "the callback task must be created in the account transaction")
	var taskID int64
	require.NoError(t, rows.Scan(&taskID))
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return taskID
}
