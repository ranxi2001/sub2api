package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRepositoryApplyOpenAIOAuthReauthIsAtomic(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	subscriptionExpiresAt := "2026-09-27T12:17:56Z"

	applied, err := repo.ApplyOpenAIOAuthReauth(
		context.Background(),
		71,
		"worker-a",
		42,
		map[string]any{"access_token": "old", "refresh_token": "old-rt"},
		map[string]any{
			"access_token":            "new",
			"refresh_token":           "new-rt",
			"subscription_expires_at": subscriptionExpiresAt,
		},
		map[string]any{"email": "user@example.com"},
	)

	require.NoError(t, err)
	require.True(t, applied)
	require.Len(t, exec.execQueries, 1)
	query := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, query, "WITH locked_task AS")
	require.Contains(t, query, "FOR UPDATE")
	require.Contains(t, query, "updated_account AS")
	require.Contains(t, query, "FROM locked_task")
	require.Contains(t, query, "task.worker_id = $10")
	require.Contains(t, query, "expires_at = COALESCE($14::timestamptz, a.expires_at)")
	require.Contains(t, query, "status = $5")
	require.NotContains(t, query, "schedulable =", "credential recovery must not overwrite the persistent scheduling switch")
	require.Contains(t, query, "rate_limited_at = NULL")
	require.Contains(t, query, "rate_limit_reset_at = NULL")
	require.Contains(t, query, "overload_until = NULL")
	require.Contains(t, query, "completed_task AS ( UPDATE openai_oauth_reauth_tasks")
	require.Contains(t, query, "INSERT INTO scheduler_outbox")
	require.Contains(t, query, "FROM completed_task")
	require.Len(t, exec.execArgs[0], 14)
	require.Equal(t, int64(71), exec.execArgs[0][8])
	require.Equal(t, "worker-a", exec.execArgs[0][9])
	require.Equal(t, service.OpenAIOAuthReauthStatusCallbackProcessing, exec.execArgs[0][10])
	require.Equal(t, service.OpenAIOAuthReauthStatusSucceeded, exec.execArgs[0][11])
	wantExpiry, err := time.Parse(time.RFC3339, subscriptionExpiresAt)
	require.NoError(t, err)
	require.Equal(t, &wantExpiry, exec.execArgs[0][13])
}

func TestAccountRepositoryApplyOpenAIOAuthReauthPreservesExpiryWithoutValidSubscriptionExpiry(t *testing.T) {
	for _, credentials := range []map[string]any{
		{"access_token": "new"},
		{"access_token": "new", "subscription_expires_at": "not-a-time"},
	} {
		exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
		repo := newAccountRepositoryWithSQL(nil, exec, nil)

		applied, err := repo.ApplyOpenAIOAuthReauth(
			context.Background(), 71, "worker-a", 42,
			map[string]any{"access_token": "old"}, credentials, nil,
		)

		require.NoError(t, err)
		require.True(t, applied)
		require.Len(t, exec.execArgs, 1)
		require.Len(t, exec.execArgs[0], 14)
		require.Nil(t, exec.execArgs[0][13])
	}
}

func TestAccountRepositoryApplyOpenAIOAuthReauthRejectsCASMiss(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)

	applied, err := repo.ApplyOpenAIOAuthReauth(
		context.Background(), 71, "wrong-worker", 42,
		map[string]any{"access_token": "stale"},
		map[string]any{"access_token": "new"},
		nil,
	)

	require.NoError(t, err)
	require.False(t, applied)
	require.Len(t, exec.execQueries, 1)
}
