//go:build integration

package qqbot

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQueuedMessageExpiresBeforeSpendingQuota(t *testing.T) {
	h := newSiteChatRetryHarness(t, "chat_completions", http.StatusOK, func(http.ResponseWriter, *http.Request, int32) { t.Error("expired job called model") })
	h.enqueue(t, "你好")
	_, err := h.db.Exec(`UPDATE qq_bot_jobs SET created_at=NOW()-interval '250 seconds' WHERE message_id='retry-question'`)
	require.NoError(t, err)
	h.s.runOne(context.Background())
	var status string
	require.NoError(t, h.db.QueryRow(`SELECT status FROM qq_bot_jobs WHERE message_id='retry-question'`).Scan(&status))
	require.Equal(t, "expired", status)
	require.Zero(t, h.calls.Load())
	var usage int
	require.NoError(t, h.db.QueryRow(`SELECT count(*) FROM qq_bot_daily_usage`).Scan(&usage))
	require.Zero(t, usage)
}
