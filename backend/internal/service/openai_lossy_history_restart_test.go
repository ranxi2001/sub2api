//go:build unit && historyshapes

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAILossyHistoryRestart(t *testing.T) {
	for _, ws := range []bool{false, true} {
		for _, body := range []string{
			`{"model":"gpt-6-astra","instructions":"current control","tools":[{"type":"function","name":"new_work"}],"previous_response_id":"resp_foreign","input":[{"role":"user","content":"current task"},{"type":"agent_message","encrypted_content":"fixture-cipher"},{"type":"function_call_output","call_id":"orphan","output":"already done"}]}`,
			`{"model":"gpt-6-astra","conversation":"foreign","input":[{"type":"agent_message","encrypted_content":"fixture-cipher"}]}`,
			`{"model":"gpt-6-astra","input":[{"type":"compaction","encrypted_content":"fixture-cipher","summary":"readable summary"},{"type":"function_call","call_id":"pending","name":"effect","arguments":"{\"n\":9007199254740993}","status":"in_progress"}]}`,
		} {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.LossyHistoryRestartEnabled = true
			svc := &OpenAIGatewayService{cfg: cfg, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			manager, c, _ := issue715WSManager(t, svc, context.Background())
			SetOpenAIWSRecoveryPrincipal(c, 715, 715)
			ctx := manager.ctx
			if !ws {
				ctx = context.Background()
			}
			next, clean, changed, err := svc.PrepareOpenAI409ReadableContinuation(ctx, c, []byte(body), "fixture-session", "gpt-6-astra")
			if gjson.Get(body, "previous_response_id").Exists() {
				require.True(t, svc.CanDiscardOpenAI409ResponsePointer(ctx, []byte(body), 715, "resp_foreign"))
			}
			require.NoError(t, err)
			require.True(t, changed)
			require.NotContains(t, string(clean), "fixture-cipher")
			require.False(t, gjson.GetBytes(clean, "previous_response_id").Exists())
			require.False(t, gjson.GetBytes(clean, "conversation").Exists())
			require.NotEmpty(t, gjson.GetBytes(clean, "input").Array())
			for _, item := range gjson.GetBytes(clean, "input").Array() {
				require.Empty(t, item.Get("type").String())
			}
			require.Equal(t, gjson.Get(body, "instructions").Raw, gjson.GetBytes(clean, "instructions").Raw)
			require.Equal(t, gjson.Get(body, "tools").Raw, gjson.GetBytes(clean, "tools").Raw)
			request := openAIAgentRecoveryFromContext(next)
			require.True(t, request.omittedHistory)
			require.Nil(t, request.omittedSource)
			require.Empty(t, request.toolProofs)
			require.Empty(t, request.keys)
			cfg.Gateway.OpenAIWS.LossyHistoryRestartEnabled = false
			_, same, changed := svc.PrepareOpenAILossyHistoryRestart(ctx, c, []byte(body), "fixture-session", "gpt-6-astra")
			require.False(t, changed)
			require.Equal(t, body, string(same))
			manager.abort()
		}
	}
}
