package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func issue914LegacyPolicy(t *testing.T, cfg *config.Config, enabled bool) {
	t.Helper()
	raw, err := json.Marshal(map[string]bool{"OpenAILegacyCompatibilityMode": enabled})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &cfg.Gateway))
}

func TestIssue914LegacyRecoveryPrecedence(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
	manager, c, account := issue715WSManager(t, svc, context.Background())
	defer manager.abort()
	body := []byte(issue842Source)
	strict := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, body, "fixture-session", "gpt-6-astra")
	require.True(t, OpenAIAgentRecoveryNoReplay(strict))
	issue914LegacyPolicy(t, svc.cfg, true)
	ctx := svc.WithOpenAIWSAgentRecovery(strict, c, body, "fixture-session", "gpt-6-astra")
	require.Nil(t, openAIAgentRecoveryFromContext(ctx), "even an inherited strict snapshot must be masked")
	require.False(t, OpenAIAgentRecoveryNoReplay(ctx))
	require.False(t, OpenAIWSAgentRecoveryBound(ctx))
	require.True(t, OpenAIWSAgentRecoveryCanRetry(ctx))
	require.Nil(t, newOpenAIWSAgentRecovery(svc, ctx, c))
	clean, err := svc.PrepareOpenAIAgentReplayBody(body)
	require.NoError(t, err)
	require.Equal(t, body, clean)
	_, clean, changed, err := svc.PrepareOpenAI409ReadableContinuation(ctx, c, body, "fixture-session", "gpt-6-astra")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, clean)
	_, clean, changed = svc.PrepareOpenAILossyHistoryRestart(ctx, c, body, "fixture-session", "gpt-6-astra")
	require.False(t, changed)
	require.Equal(t, body, clean)
	_, clean, changed, err = svc.RebuildOpenAIWSUnsentTurn(ctx, c, body, "fixture-session", "gpt-6-astra")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, clean)
	// Changed controls/input and compaction must not reintroduce a recovery gate.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.invalid/v1/responses", strings.NewReader(`{"input":[],"instructions":"compatibility projection"}`))
	require.NoError(t, err)
	finish, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
	require.NoError(t, err)
	finish(true)
	require.NoError(t, svc.checkOpenAIAgentRecoveryCompaction(ctx, account, req))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = svc.prepareOpenAIAgentRecoveryAttempt(canceled, account, req)
	require.ErrorIs(t, err, context.Canceled)
	// Disabling legacy returns to the original policy rather than changing opt-out.
	issue914LegacyPolicy(t, svc.cfg, false)
	strict = svc.WithOpenAIAgentMessageRecovery(ctx, c, body, "fixture-session", "gpt-6-astra")
	require.True(t, OpenAIAgentRecoveryNoReplay(strict))
}

func TestIssue914ImageCompatibilityPreservesNumbers(t *testing.T) {
	body := []byte(`{"model":"gpt-5.1","tools":[{"type":"image_generation","format":"png"}],"input":[{"role":"user","content":"fixture","sequence":9007199254740993}]}`)
	for _, legacy := range []bool{false, true} {
		cfg := &config.Config{}
		issue914LegacyPolicy(t, cfg, legacy)
		account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		got, changed, err := normalizeOpenAIResponsesWebSocketCompatibilityBodyWithConfig(cfg, body, account, false)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, "9007199254740993", gjson.GetBytes(got, "input.0.sequence").Raw)
		require.Equal(t, "png", gjson.GetBytes(got, "tools.0.output_format").String())
	}
}
