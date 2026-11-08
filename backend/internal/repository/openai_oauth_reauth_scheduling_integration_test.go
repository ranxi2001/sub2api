//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIReauthPreserves429SchedulingPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		extra  map[string]any
		before bool
		want   bool
	}{
		{name: "legacy paused account", want: true},
		{name: "opted out", extra: map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: false}, want: true},
		{name: "429 paused account", extra: map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: true}},
		{name: "opted in active account", extra: map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: true}, before: true, want: true},
		{name: "string flag is not enabled", extra: map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: "true"}, want: true},
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
			rows, err := tx.Client().QueryContext(ctx, `INSERT INTO openai_oauth_reauth_tasks
 (account_id,status,stage,expected_credentials_hash,worker_id)
 VALUES ($1,$2,$3,'synthetic-hash','worker') RETURNING id`,
				a.ID, service.OpenAIOAuthReauthStatusCallbackProcessing, service.OpenAIOAuthReauthStageApplyingCredentials)
			require.NoError(t, err)
			t.Cleanup(func() { _ = rows.Close() })
			require.True(t, rows.Next(), "the callback task must be created in the account transaction")
			var taskID int64
			require.NoError(t, rows.Scan(&taskID))
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())

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
