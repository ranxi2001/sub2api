//go:build unit && followupcapture

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestOpenAIWSUnsentHistoryDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, input, state, reason string }{
		{"unreadable_history", `[{"type":"agent_message","encrypted_content":"fixture-private-cipher"}]`, "source_unsupported", "history_unready"},
		{"wrong_response", `[{"role":"user","content":"fixture-private-task"}]`, "ready", "response_anchor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, entries := observer.New(zap.WarnLevel)
			ctx := logger.IntoContext(context.Background(), zap.New(core))
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			manager, c, _ := issue715WSManager(t, svc, ctx)
			defer manager.abort()
			SetOpenAIWSRecoveryPrincipal(c, 715, 715)
			manager.request = openAIAgentRecoveryFromContext(manager.ctx)
			manager.stageLocalHistory([]byte(`{"model":"gpt-6-astra","input":` + tc.input + `}`))
			manager.completedBridge.response = "resp_fixture_private"
			manager.commitLocalHistory(true)
			current := []byte(`{"type":"response.create","model":"gpt-6-astra","previous_response_id":"resp_fixture_other","input":[{"type":"function_call_output","call_id":"fixture-private-call","output":"fixture-private-result"}]}`)
			cause := openAIWSUnsentTurn(denyOpenAITurn("account_ineligible"), current, "gpt-6-astra", 7)
			require.Same(t, cause, manager.restoreUnsentHistory(cause))
			logs := entries.FilterMessage("openai.ws_unsent_history_restore_skipped").All()
			require.Len(t, logs, 1)
			fields := logs[0].ContextMap()
			require.Equal(t, tc.state, fields["history_state"])
			require.Equal(t, tc.reason, fields["restore_reason"])
			require.Equal(t, int64(7), fields["turn"])
			for name := range fields {
				require.Contains(t, []string{"restore_reason", "history_state", "history_input_failure", "turn", "history_items", "history_bytes", "history_ready", "delivery_pending", "source_previous_present"}, name)
			}
			raw, err := json.Marshal(fields)
			require.NoError(t, err)
			for _, secret := range []string{"fixture-private", "resp_fixture", "encrypted_content", "call_id"} {
				require.NotContains(t, string(raw), secret)
			}
		})
	}
}
