package service

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func issue715OutputAttempt(t *testing.T, svc *OpenAIGatewayService, parent context.Context) (context.Context, *gin.Context, func(bool)) {
	t.Helper()
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
	ctx := svc.WithOpenAIAgentMessageRecovery(parent, c, []byte(`{"input":[]}`), "fixture-session", "gpt-6-astra")
	c.Request = c.Request.WithContext(ctx)
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewBufferString(`{"input":[]}`))
	require.NoError(t, err)
	finish, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
	require.NoError(t, err)
	return ctx, c, finish
}

func TestIssue715OutputPendingIsNeverACompatibilityGrant(t *testing.T) {
	for _, outcome := range []string{"commit", "abort", "cancel", "early_only"} {
		t.Run(outcome, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx, c, finish := issue715OutputAttempt(t, svc, parent)
			item := `{"type":"agent_message","encrypted_content":"fixture-output"}`
			require.NoError(t, observeOpenAIAgentRecoveryOutput(c, []byte(`{"type":"response.output_item.added","item":`+item+`}`), true))
			r := openAIAgentRecoveryFromContext(ctx)
			require.Len(t, r.delivery.keys, 1, "early complete opaque item must be fenced before exposure")
			var key [32]byte
			for k := range r.delivery.keys {
				key = k
			}
			short, stop := context.WithTimeout(context.Background(), time.Millisecond)
			_, _, err := svc.awaitOpenAIAgentRecovery(short, nil, &svc.agentMessageRecovery, key)
			stop()
			require.Error(t, err, "pending must wait or fail, never authorize dispatch")
			if outcome != "early_only" {
				require.NoError(t, observeOpenAIAgentRecoveryOutput(c, []byte(`{"status":"completed","output":[`+item+`]}`), false))
			}
			if outcome == "cancel" {
				cancel()
			}
			finish(outcome != "abort")
			finish(false) // deferred cleanup is idempotent
			binding, found, err := svc.awaitOpenAIAgentRecovery(context.Background(), nil, &svc.agentMessageRecovery, key)
			if outcome == "commit" {
				require.NoError(t, err)
				require.True(t, found)
				require.Empty(t, binding.pending)
				require.False(t, binding.failed)
			} else {
				require.Error(t, err, "failure and early-only output must not become first-seen or a completed owner")
			}
		})
	}
}

func TestIssue715PendingLookupHonorsCancellationAndMissingFence(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	ctx, c, finish := issue715OutputAttempt(t, svc, context.Background())
	defer finish(false)
	require.NoError(t, observeOpenAIAgentRecoveryOutput(c, []byte(`{"status":"completed","output":[{"type":"agent_message","encrypted_content":"fixture-output"}]}`), false))
	var key [32]byte
	for k := range openAIAgentRecoveryFromContext(ctx).delivery.keys {
		key = k
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := svc.awaitOpenAIAgentRecovery(canceled, nil, &svc.agentMessageRecovery, key)
	require.Error(t, err)
}
