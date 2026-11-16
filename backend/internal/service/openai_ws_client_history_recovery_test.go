//go:build unit && historyshapes

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWSClientHistoryRestart(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","instructions":"fixture current control","tools":[{"type":"function","name":"new_work","parameters":{"type":"object"}}],"input":[{"role":"user","content":"fixture continue original task"},{"type":"agent_message","content":[{"type":"input_text","text":"fixture delegated task"}],"encrypted_content":"fixture-old-cipher"},{"type":"function_call","call_id":"fixture-old-call","name":"fixture_side_effect","arguments":"{\"n\":9007199254740993}","status":"completed"},{"type":"function_call_output","call_id":"fixture-old-call","output":"fixture already applied exactly once"}]}`)
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.ClientHistoryRecoveryEnabled = enabled
			svc := &OpenAIGatewayService{cfg: cfg, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			manager, c, _ := issue715WSManager(t, svc, context.Background())
			SetOpenAIWSRecoveryPrincipal(c, 715, 715)
			defer manager.abort()
			ctx := svc.WithOpenAIWSAgentRecovery(manager.ctx, c, body, "fixture-session", "gpt-6-astra")
			next, clean, changed, err := svc.PrepareOpenAI409ReadableContinuation(ctx, c, body, "fixture-session", "gpt-6-astra")
			if !enabled {
				require.Error(t, err)
				require.False(t, changed)
				return
			}
			require.NoError(t, err, "CLIENT_HISTORY_RESTART_REFUSED")
			require.True(t, changed)
			require.Contains(t, string(clean), "fixture delegated task")
			require.Contains(t, string(clean), "fixture already applied exactly once")
			require.Contains(t, string(clean), "9007199254740993")
			require.NotContains(t, string(clean), "fixture-old-cipher")
			for _, item := range gjson.GetBytes(clean, "input").Array() {
				require.NotContains(t, item.Get("type").String(), "call")
			}
			require.JSONEq(t, gjson.GetBytes(body, "tools").Raw, gjson.GetBytes(clean, "tools").Raw)
			require.Equal(t, gjson.GetBytes(body, "instructions").Raw, gjson.GetBytes(clean, "instructions").Raw)
			require.Contains(t, string(clean), "unverified data")
			request := openAIAgentRecoveryFromContext(next)
			require.True(t, request.omittedHistory)
			require.Empty(t, request.toolProofs)
			require.Nil(t, request.omittedSource)
			require.Empty(t, svc.agentMessageRecovery.toolProofs, "client reports never mint tool proof")
		})
	}
}
